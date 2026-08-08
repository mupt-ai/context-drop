const OLD_INCOMING_MARKER = "\nThe incoming text:\n\n";
const MAX_HISTORICAL_TEXT = 4000;
const MAX_HISTORICAL_MESSAGES = 16;
const DURABLE_ANCHOR =
  "Earlier conversation and tool history remains durable in the Pi session JSONL and in the memory/archive paths named below. " +
  "That older bulk context is omitted from this provider request for latency. Read the durable sources before answering whenever the current request depends on omitted details.\n\n";

function textFromContent(content) {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content
    .filter((block) => block?.type === "text" && typeof block.text === "string")
    .map((block) => block.text)
    .join("");
}

function bounded(text) {
  if (text.length <= MAX_HISTORICAL_TEXT) return text;
  return `${text.slice(0, MAX_HISTORICAL_TEXT)}\n[Older message truncated in working context; full text remains in the durable session/archive.]`;
}

function asTextContent(message, text) {
  return { ...message, content: [{ type: "text", text }] };
}

function normalizeHistoricalUser(message) {
  const text = textFromContent(message.content);
  const marker = text.lastIndexOf(OLD_INCOMING_MARKER);
  const incoming = marker >= 0 ? text.slice(marker + OLD_INCOMING_MARKER.length) : text;
  return asTextContent(message, `Historical incoming iMessage:\n\n${bounded(incoming.trim())}`);
}

function normalizeHistoricalAssistant(message) {
  if (message.stopReason !== "stop") return undefined;
  const text = bounded(textFromContent(message.content).trim());
  if (!text) return undefined;
  return asTextContent(message, text);
}

function prependAnchor(message) {
  return asTextContent(message, DURABLE_ANCHOR + textFromContent(message.content));
}

export function compactContext(messages) {
  let latestUser = -1;
  for (let i = messages.length - 1; i >= 0; i--) {
    if (messages[i]?.role === "user") {
      latestUser = i;
      break;
    }
  }
  if (latestUser < 0) return messages;

  let omittedDurableSummary = false;
  const historical = [];
  for (let i = 0; i < latestUser; i++) {
    const message = messages[i];
    switch (message?.role) {
      case "compactionSummary":
      case "branchSummary":
        omittedDurableSummary = true;
        break;
      case "user":
        historical.push(normalizeHistoricalUser(message));
        break;
      case "assistant": {
        const normalized = normalizeHistoricalAssistant(message);
        if (normalized) historical.push(normalized);
        break;
      }
      case "custom":
        if (textFromContent(message.content).length <= MAX_HISTORICAL_TEXT) historical.push(message);
        break;
    }
  }

  const recentHistorical = historical.slice(-MAX_HISTORICAL_MESSAGES);
  const current = messages.slice(latestUser);
  if (omittedDurableSummary && current.length > 0) {
    current[0] = prependAnchor(current[0]);
  }
  return [...recentHistorical, ...current];
}

export default function contextDropIMessageContext(pi) {
  pi.on("context", (event) => ({ messages: compactContext(event.messages) }));
}
