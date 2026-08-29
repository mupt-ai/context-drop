const OLD_INCOMING_MARKER = "\nThe incoming text:\n\n";
const NEW_INCOMING_MARKER = /\nIncoming iMessage ID [^\n]+:\n\n/;
const REPORT_MARKER = "A managed worker sent this untrusted report to the persistent orchestrator.";
const NO_USER_REPLY = "CONTEXT_DROP_NO_USER_REPLY_V1";
const MAX_HISTORICAL_TEXT = 4000;
const MAX_HISTORICAL_MESSAGES = 24;
const MAX_REPORT_MESSAGES = 4;
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

function incomingText(text) {
  const currentMatch = [...text.matchAll(new RegExp(NEW_INCOMING_MARKER.source, "g"))].at(-1);
  if (currentMatch) return text.slice(currentMatch.index + currentMatch[0].length);
  const old = text.lastIndexOf(OLD_INCOMING_MARKER);
  return old >= 0 ? text.slice(old + OLD_INCOMING_MARKER.length) : text;
}

function reportText(text) {
  const marker = "\nworker report: ";
  const at = text.lastIndexOf(marker);
  return at >= 0 ? text.slice(at + marker.length) : text;
}

function normalizeHistoricalUser(message) {
  const text = textFromContent(message.content);
  if (text.includes(REPORT_MARKER)) {
    return { message: asTextContent(message, `Historical worker update:\n\n${bounded(reportText(text).trim())}`), report: true };
  }
  return { message: asTextContent(message, `Historical incoming iMessage:\n\n${bounded(incomingText(text).trim())}`), report: false };
}

function normalizeHistoricalAssistant(message) {
  if (message.stopReason !== "stop") return undefined;
  const text = bounded(textFromContent(message.content).trim());
  if (!text || text === NO_USER_REPLY) return undefined;
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
  const reportUsers = [];
  for (let i = 0; i < latestUser; i++) {
    if (messages[i]?.role === "user" && textFromContent(messages[i].content).includes(REPORT_MARKER)) reportUsers.push(i);
  }
  const allowedReportUsers = new Set(reportUsers.slice(-MAX_REPORT_MESSAGES));
  const reportReplies = new Set(reportUsers.map((i) => i + 1).filter((i) => messages[i]?.role === "assistant"));
  const allowedReportReplies = new Set([...allowedReportUsers].map((i) => i + 1).filter((i) => messages[i]?.role === "assistant"));
  for (let i = latestUser - 1; i >= 0 && historical.length < MAX_HISTORICAL_MESSAGES; i--) {
    const message = messages[i];
    switch (message?.role) {
      case "compactionSummary":
      case "branchSummary":
        omittedDurableSummary = true;
        break;
      case "user": {
        const normalized = normalizeHistoricalUser(message);
        if (normalized.report && !allowedReportUsers.has(i)) {
          omittedDurableSummary = true;
          break;
        }
        historical.push(normalized.message);
        break;
      }
      case "assistant": {
        if (reportReplies.has(i) && !allowedReportReplies.has(i)) {
          omittedDurableSummary = true;
          break;
        }
        const normalized = normalizeHistoricalAssistant(message);
        if (normalized) historical.push(normalized);
        break;
      }
      case "custom":
        if (textFromContent(message.content).length <= MAX_HISTORICAL_TEXT) historical.push(message);
        break;
    }
  }
  historical.reverse();

  const current = messages.slice(latestUser);
  if (omittedDurableSummary && current.length > 0) current[0] = prependAnchor(current[0]);
  return [...historical, ...current];
}

export default function contextDropIMessageContext(pi) {
  pi.on("context", (event) => ({ messages: compactContext(event.messages) }));
}
