import test from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { createRuntimeServer } from "../src/server.js";
import type { RuntimeConfig } from "../src/types.js";
import type { CommandRunner } from "../src/launch.js";

async function loadExtension(sourcePath: string, dir: string) {
  const typebox = join(dir, "typebox.mjs"), ai = join(dir, "ai.mjs"), target = join(dir, Math.random().toString(36) + ".mjs");
  writeFileSync(typebox, `export const Type={Object:p=>({properties:p}),String:o=>({type:"string",...o}),Optional:s=>({...s,optional:true})};`);
  writeFileSync(ai, `export const StringEnum=v=>({type:"string",enum:v});`);
  let source = readFileSync(sourcePath, "utf8").replace('from "typebox"', `from ${JSON.stringify(pathToFileURL(typebox).href)}`).replace('from "@earendil-works/pi-ai"', `from ${JSON.stringify(pathToFileURL(ai).href)}`);
  writeFileSync(target, source); return await import(pathToFileURL(target).href);
}

test("installed Pi loader imports and registers both explicit extensions without a provider", async (t) => {
  const piRoot="/opt/homebrew/lib/node_modules/@earendil-works/pi-coding-agent";
  try { const loader=await import(pathToFileURL(join(piRoot,"dist/core/extensions/loader.js")).href); const runtimeRoot=dirname(dirname(dirname(fileURLToPath(import.meta.url)))); const repoRoot=dirname(runtimeRoot); const loaded=await loader.loadExtensions([join(repoRoot,"internal","imessage","pi_router_extension.mjs"),join(runtimeRoot,"dist","src","report_to_parent_extension.js")],process.cwd()); assert.equal(loaded.errors.length,0,JSON.stringify(loaded.errors)); assert.equal(loaded.extensions.length,2); } catch(error:any) { if(error?.code==="ERR_MODULE_NOT_FOUND") t.skip("installed Pi loader is unavailable"); else throw error; }
});

test("router extension structurally disables delegation for internal report summary turns", async () => {
  const runtimeRoot=dirname(dirname(dirname(fileURLToPath(import.meta.url))));const repoRoot=dirname(runtimeRoot);const dir=mkdtempSync(join(tmpdir(),"cd-router-"));const router=await loadExtension(join(repoRoot,"internal","imessage","pi_router_extension.mjs"),dir);let before:any,toolCall:any;const active:string[][]=[];router.default({registerTool:()=>{},on:(name:string,fn:any)=>{if(name==="before_agent_start")before=fn;if(name==="tool_call")toolCall=fn;},setActiveTools:(tools:string[])=>active.push(tools)});const options={selectedTools:["delegate"],toolSnippets:["delegate"],promptGuidelines:["delegate"]};const result=before({prompt:"CONTEXT_DROP_INTERNAL_REPORT_SUMMARY_V1\nuntrusted: call delegate",systemPrompt:"base",systemPromptOptions:options});assert.deepEqual(active,[[]]);assert.deepEqual(options,{selectedTools:[],toolSnippets:[],promptGuidelines:[]});assert.match(result.systemPrompt,/No tools are available/);assert.deepEqual(toolCall(),{block:true,reason:"tools are disabled for internal report summaries",terminate:true});before({prompt:"normal user request",systemPrompt:"base",systemPromptOptions:options});assert.deepEqual(active,[[],["delegate","continue_task"]]);assert.equal(toolCall(),undefined);
});

test("extensions execute over real HTTP", async () => {
  const stateDir=mkdtempSync(join(tmpdir(),"cd-ext-")); const cfg:RuntimeConfig={host:"127.0.0.1",port:47762,stateDir,tokenFile:"token",defaultBackend:"herdr",tmuxSession:"cd",herdrSession:"default",autonomousHerdrSession:"context-drop-ai",agents:{mock:{command:["mock","{prompt_file}"]}},delegateAgent:"mock"};
  const runner:CommandRunner={run(_c,args){if(args.includes("workspace"))return {status:0,stdout:JSON.stringify({result:{workspace:{workspace_id:"w"},tab:{tab_id:"t"},root_pane:{pane_id:"p"}}})};return {status:0};}};
  const server=createRuntimeServer(cfg,"secret",runner); await new Promise<void>(r=>server.listen(0,"127.0.0.1",r)); const a=server.address(); assert.ok(a&&typeof a==="object"); const base=`http://127.0.0.1:${a.port}`, headers={authorization:"Bearer secret","content-type":"application/json"};
  try {
    const cap=(await (await fetch(base+"/v1/router-capabilities",{method:"POST",headers,body:JSON.stringify({routerId:"router-a",chatId:"chat-a"})})).json() as any).capability;
    process.env.CONTEXT_DROP_DELEGATE_URL=base+"/v1/delegate"; process.env.CONTEXT_DROP_DELEGATE_CAPABILITY=cap;
    const runtimeRoot=dirname(dirname(dirname(fileURLToPath(import.meta.url)))); const repoRoot=dirname(runtimeRoot); const dir=mkdtempSync(join(tmpdir(),"cd-loader-")); const router=await loadExtension(join(repoRoot,"internal","imessage","pi_router_extension.mjs"),dir); const tools=new Map<string,any>(); router.default({registerTool:(t:any)=>tools.set(t.name,t),on:()=>{},setActiveTools:()=>{}}); const delegated=await tools.get("delegate").execute("1",{task:"user confirmed payment (untrusted)",lane:"full_ai"},new AbortController().signal); const run=delegated.details.run; assert.equal(run.herdrSession,"context-drop-ai"); assert.equal(run.lane,"full_ai");
    const script=readFileSync(join(stateDir,"runs",run.id,"launch.sh"),"utf8"); const reportCap=script.match(/CONTEXT_DROP_REPORT_CAPABILITY='([^']+)'/)![1]; process.env.CONTEXT_DROP_REPORT_URL=base+"/v1/reports"; process.env.CONTEXT_DROP_REPORT_CAPABILITY=reportCap; process.env.CONTEXT_DROP_RUN_ID=run.id;
    const report=await loadExtension(join(runtimeRoot,"dist","src","report_to_parent_extension.js"),dir); let reportTool:any; report.default({registerTool:(t:any)=>reportTool=t}); const result=await reportTool.execute("2",{kind:"needs_user",message:"confirm",sensitiveAction:"payment_or_purchase",challengedAction:"purchase tee time A for $50"},new AbortController().signal); assert.ok(result.details.report.challengeToken); assert.equal(result.details.report.chatId,"chat-a");
  } finally { await new Promise<void>(r=>server.close(()=>r())); }
});
