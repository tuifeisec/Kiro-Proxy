// 仅构造可审查候选，永不写入 Kiro 安装文件。应用候选需另获用户明确授权。
import { readFileSync, writeFileSync, mkdirSync, existsSync } from 'node:fs'
import { resolve, dirname } from 'node:path'
import { createHash } from 'node:crypto'
import { Script } from 'node:vm'
import { fileURLToPath } from 'node:url'

export const ORIGINAL_SHA256 = '6acca01dfa75f7a301618f59736c996b481843445838bf618499487194d35fbb'
export const BRIDGE_ENV = 'KIRO_PROXY_STAGE_BRIDGE'
export const sha256 = (value) => createHash('sha256').update(value).digest('hex')

// 新的 call option 避开 LangChain 对 metadata 的 runnable-config 分离。
// 两条 transport 都用实际 conversationState 绑定，不用本地 sessionId 回查。
export const BRIDGE_SOURCE = `
const kiroProxyRunSourcesV2=new Map;
function kiroProxyDutyHelpV2(){return " Fixed duty keys: orchestration, routine_support, investigation, requirements, design, implementation_plan, implementation, iterative_execution, experiment, design_review, code_review, semantic_review, review_synthesis, review_verdict, validation, delivery_audit, operations, pr_lifecycle. The 20-duty layout replaces routine_support with preparation/finalization and pr_lifecycle with pr_submit/pr_feedback. Use a configured key; unknown declarations use the fixed default target.";}
const kiroProxyBuiltinDutiesV2={
 "bundled://autoresearch":{"experiments/experiment":"experiment"},
 "bundled://feature-pipeline":{"setup":"routine_support","requirements":"requirements","design-loop/design-draft":"design","design-loop/design-review":"design_review","plan":"implementation_plan","code-loop/implement":"implementation","code-loop/code-reviews/review-fable":"code_review","code-loop/code-reviews/review-gpt":"code_review","code-loop/code-aggregate":"review_verdict","validate-gate/validate":"validation"},
 "bundled://goal":{"goal-loop/work":"iterative_execution"},
 "bundled://investigate":{"investigate":"investigation"},
 "bundled://publish-pr":{"submit":"pr_lifecycle","pr-loop/respond":"pr_lifecycle"},
 "bundled://ralph":{"ralph-loop/iterate":"iterative_execution"},
 "bundled://semantic-review-multi-model":{"setup":"routine_support","reviews/review-opus":"semantic_review","reviews/review-gpt":"semantic_review","aggregate":"review_synthesis"}
};
function kiroProxyRecordRunV2(id,source){if(process.env.KIRO_PROXY_STAGE_BRIDGE==="1")kiroProxyRunSourcesV2.set(id,{...source});}
function kiroProxyStampMainExecutionV2(session,execution){if(process.env.KIRO_PROXY_STAGE_BRIDGE==="1"&&!session.workflowId&&session.kiroProxyRoute&&typeof execution.executionId==="string")session.kiroProxyRoute={...session.kiroProxyRoute,executionId:execution.executionId};}
function kiroProxyAgentRevisionV2(workflow,taskType){let definition={...workflow};delete definition.planRevision;return require("node:crypto").createHash("sha256").update(JSON.stringify({workflow:definition,taskType})).digest("hex");}
function kiroProxySingleRunV2(workflow){return workflow?.steps?.length===1&&workflow.steps[0]?.type==="step"&&workflow.steps[0]?.id==="run";}
function kiroProxyTextV2(value,key,max){if(typeof value!=="string"||Buffer.byteLength(value,"utf8")>max||value!==value.trim()||value.includes(String.fromCharCode(0))||value.includes(String.fromCharCode(10))||value.includes(String.fromCharCode(13)))throw new Error("Kiro proxy "+key+" is invalid");return value;}
function kiroProxySourceFileV2(base,id){return Mn.join(fH(base,id),"kiro-proxy-routing-v2.json");}
function kiroProxyValidateSourceV2(record,id){
 if(!record||typeof record!=="object"||Array.isArray(record)||record.version!==2||record.workflowId!==id)throw new Error("Kiro proxy workflow source record has invalid version or workflowId");
 for(let key of Object.keys(record))if(!["version","workflowId","definitionId","definitionRevision","taskType","bundleSha","parentRoute"].includes(key))throw new Error("Kiro proxy workflow source record has unknown "+key);
 for(let [key,max] of [["workflowId",512],["definitionId",1024],["definitionRevision",256],["taskType",128],["bundleSha",64]])kiroProxyTextV2(record[key],"workflow source "+key,max);
 if(record.parentRoute!==undefined){if(!record.parentRoute||typeof record.parentRoute!=="object"||Array.isArray(record.parentRoute))throw new Error("Kiro proxy parent route is invalid");for(let key of Object.keys(record.parentRoute))if(!["nodeId","definitionId","definitionRevision","nodePath","taskType","executionId","workflowId"].includes(key))throw new Error("Kiro proxy parent route has unknown "+key);for(let [key,max] of [["nodeId",128],["definitionId",1024],["definitionRevision",256],["nodePath",1024],["taskType",128],["executionId",512],["workflowId",512]])if(record.parentRoute[key]!==undefined)kiroProxyTextV2(record.parentRoute[key],"parent route "+key,max);}
 return record;
}
async function kiroProxyWriteSourceV2(fs,base,id,source){
 if(process.env.KIRO_PROXY_STAGE_BRIDGE!=="1")return;
 let record=kiroProxyValidateSourceV2({version:2,workflowId:id,definitionId:source.definitionId??"",definitionRevision:source.definitionRevision??"",taskType:source.taskType??"",bundleSha:source.bundleSha??"",...source.parentRoute?{parentRoute:{...source.parentRoute}}:{}},id),path=kiroProxySourceFileV2(base,id),temporary=path+".tmp."+Ix.randomUUID();
 try{await fs.writeFile(temporary,JSON.stringify(record));await fs.rename(temporary,path)}catch(error){try{await fs.unlink(temporary)}catch{}throw error}
 let verified=kiroProxyValidateSourceV2(JSON.parse(await fs.readFile(path)),id);
 if(JSON.stringify(verified)!==JSON.stringify(record))throw new Error("Kiro proxy workflow source record verification failed");
 kiroProxyRecordRunV2(id,verified);
}
async function kiroProxyReadSourceV2(fs,base,id){
 if(process.env.KIRO_PROXY_STAGE_BRIDGE!=="1")return;
 let value;try{value=await fs.readFile(kiroProxySourceFileV2(base,id))}catch(error){if(error?.code==="ENOENT")return;throw error}
 let record=kiroProxyValidateSourceV2(JSON.parse(value),id);kiroProxyRecordRunV2(id,record);return record;
}
function kiroProxyDefinitionV2(workflow){
 if(!workflow||typeof workflow!=="object")return;
 let copy={...workflow};delete copy.planRevision;
 let serialized=JSON.stringify(copy);
 if(typeof serialized!=="string")return;
 for(let recipe of hoe())if(JSON.stringify(recipe.workflow)===serialized)return{definitionId:"bundled://"+recipe.name,bundleSha:"6acca01dfa75f7a301618f59736c996b481843445838bf618499487194d35fbb"};
 if(/^agent-/.test(workflow.name)&&kiroProxySingleRunV2(workflow))return;
 return{definitionId:"content://"+require("node:crypto").createHash("sha256").update(serialized).digest("hex")};
}
function kiroProxyNodePathV2(path,workflowId,definition,nodeId){
 if(!Array.isArray(path)||path.length<2||path[0]!==workflowId||!Array.isArray(definition?.steps))return "";
 let children=definition.steps,parts=[],index=1;
 while(index<path.length){
  let segment=path[index++];if(typeof segment!=="string"||!Array.isArray(children))return "";
  let matches=children.filter(node=>node&&typeof node==="object"&&node.id===segment);
  if(matches.length!==1)return "";
  let node=matches[0],encoded;try{encoded=encodeURIComponent(node.id)||"%"}catch{return ""}
  parts.push(encoded);
  if(node.type==="step"){
   if(index!==path.length||node.id!==nodeId)return "";
   let result=parts.join("/");return Buffer.byteLength(result,"utf8")<=1024?result:"";
  }
  if(node.type==="repeat"){
   if(index>=path.length||typeof path[index]!=="string"||!/^iter-\\d+$/.test(path[index++]))return "";
   children=node.steps;
  }else if(node.type==="sequence")children=node.steps;
  else if(node.type==="parallel")children=node.branches;
  else return "";
 }
 return "";
}
function kiroProxyStepRouteV2(meta,sessionId,revision){
 let path=kiroProxyNodePathV2(meta.nodePath,meta.workflowId,meta.kiroProxyDefinition,meta.nodeId),recorded=kiroProxyRunSourcesV2.get(meta.workflowId),agentSource=recorded?.parentRoute!==undefined,singleRun=agentSource&&meta.nodeId==="run"&&path==="run"&&kiroProxySingleRunV2(meta.kiroProxyDefinition);
 let source=singleRun?{...recorded,definitionRevision:recorded.taskType?kiroProxyAgentRevisionV2(meta.kiroProxyDefinition,recorded.taskType):""}:revision===0&&!agentSource?recorded:undefined;
 source??=kiroProxyDefinitionV2(meta.kiroProxyDefinition);
 if(source?.parentRoute&&!source.taskType)return{...source.parentRoute,executionId:source.parentRoute.executionId??"",workflowId:meta.workflowId??""};
 let bundled=source?.definitionId?.startsWith("bundled://")&&revision===0&&source.bundleSha==="6acca01dfa75f7a301618f59736c996b481843445838bf618499487194d35fbb";
 let duty=bundled?kiroProxyBuiltinDutiesV2[source.definitionId]?.[path]:undefined;
 return{nodeId:typeof meta.nodeId==="string"?meta.nodeId:"",definitionId:source?.definitionId??"",definitionRevision:source?(source.definitionRevision|| (bundled?source.bundleSha+":0":String(revision??""))):"",nodePath:path,taskType:duty??source?.taskType??"",executionId:sessionId??"",workflowId:meta.workflowId??""};
}
function kiroProxyEnvelopeV2(state,mode,workflow){
 if(process.env.KIRO_PROXY_STAGE_BRIDGE!=="1"||workflow?.autoDomain!==true)return;
 if(state.currentMessage?.userInputMessage?.modelId!=="auto")throw new Error("Kiro proxy Auto domain requires an Auto request model");
 if(typeof state.conversationId!=="string"||!state.conversationId.trim())throw new Error("Kiro proxy workflow requires a request conversationId");
 let continuation=state.agentContinuationId??"";
 if(typeof continuation!=="string")throw new Error("Kiro proxy continuation identity must be a string");
 let fields={version:2,nodeId:workflow.nodeId??"",definitionId:workflow.definitionId??"",definitionRevision:workflow.definitionRevision??"",nodePath:workflow.nodePath??"",taskType:workflow.taskType??"",executionId:workflow.executionId??"",workflowId:workflow.workflowId??"",conversationId:state.conversationId,agentMode:typeof mode==="string"?mode:"",agentContinuationId:continuation};
 for(let [key,max] of [["nodeId",128],["definitionId",1024],["definitionRevision",256],["nodePath",1024],["taskType",128],["executionId",512],["workflowId",512],["conversationId",512],["agentMode",128],["agentContinuationId",512]])kiroProxyTextV2(fields[key],"workflow "+key,max);
 return fields;
}
function kiroProxyStageHeadersV2(state,mode,workflow){
 let meta=kiroProxyEnvelopeV2(state,mode,workflow);
 return meta?{"X-Kiro-Proxy-Workflow":JSON.stringify(meta)}:{};
}
function kiroProxyAttachStageV2(command,state,mode,workflow){
 let headers=kiroProxyStageHeadersV2(state,mode,workflow);
 if(Object.keys(headers).length)command.middlewareStack.add(next=>async args=>{args.request.headers={...args.request.headers,...headers};return next(args)},{step:"finalizeRequest",priority:"low",name:"kiroProxyStageV2"});
 return command;
}
function kiroProxyStageRawBodyV2(body,state,mode,workflow){
 let meta=kiroProxyEnvelopeV2(state,mode,workflow);
 if(!meta)return body;
 if(body.model!=="auto")throw new Error("Kiro proxy Auto domain requires an Auto raw model");
 let results=state.currentMessage?.userInputMessage?.userInputMessageContext?.toolResults??[];
 if(!Array.isArray(results))throw new Error("Kiro proxy current toolResults must be an array");
 return{...body,metadata:{...body.metadata,"kiro:conversation_id":meta.conversationId,"kiro:agent_mode":meta.agentMode,"kiro:proxy_continuation_id":meta.agentContinuationId,"kiro:proxy_current_tool_results":results.length}};
}
`

const sessionAnchor = 'buildSessionServices(e,r){e.policySession.authority.requireAvailable();let n=r??this.requireSessionModelId(e);return{workspace:e.workspace,'
const optionsAnchor = 'metadata:{sessionId:this.chatSessionId,agentName:this.agentName,executionId:this.executionId,chatId:h},additionalModelRequestFields:'
const rawStart = 'function sFu(t){let{conversationState:e,systemPrompt:r,modelId:n,origin:s,profileArn:a,agentMode:l,additionalModelRequestFields:u}=t,d=iFu(e),p={};return '
const rawEnd = '...Object.keys(p).length>0&&{metadata:p},...aFu(u)}}function aFu(t)'

export const PATCHES = [
 ['stage-functions', 'function B6o(t,e){', BRIDGE_SOURCE + 'function B6o(t,e){'],
 ['workflow-source', 'if(A!==void 0)try{await t.host.stateFs.unlink(A)}catch(F){T.warn("workflow.new.generated_cleanup_failed"', 'if(process.env.KIRO_PROXY_STAGE_BRIDGE==="1"&&C==="auto"){let kiroProxySource=d.workflowPath?.startsWith("agent://")?{definitionId:d.taskType?d.workflowPath:"",definitionRevision:d.taskType?kiroProxyAgentRevisionV2(g,d.taskType):"",taskType:d.taskType??"",parentRoute:t.host.getSession(p)?.kiroProxyRoute??{}}:kiroProxyDefinitionV2(g)??{};await kiroProxyWriteSourceV2(t.host.stateFs,v,S,kiroProxySource)}if(A!==void 0)try{await t.host.stateFs.unlink(A)}catch(F){T.warn("workflow.new.generated_cleanup_failed"'],
 ['workflow-source-load', 'let p=zEe(n);tWu(n),', 'process.env.KIRO_PROXY_STAGE_BRIDGE==="1"&&n.parentModelId==="auto"&&await kiroProxyReadSourceV2(this.deps.fs,r,e);let p=zEe(n);tWu(n),'],
 ['run-workflow-task-schema', 'FKu=b.object({workflowPath:b.string().nullish().describe(', 'FKu=b.object({taskType:b.string().min(1).max(64).regex(/^[a-z][a-z0-9_]*$/).optional().describe("Explicit duty for an agent:// single-step run; omit to inherit the parent Auto context when available."+kiroProxyDutyHelpV2()),workflowPath:b.string().nullish().describe('],
 ['run-workflow-task-params', 'function QFi(t,e,r,n){let s={workflowPath:t,inputs:e,parentSessionId:r};return n!==void 0&&(s.runLabel=n),s}', 'function QFi(t,e,r,n,s){let a={workflowPath:t,inputs:e,parentSessionId:r};return n!==void 0&&(a.runLabel=n),s!==void 0&&(a.taskType=s),a}'],
 ['run-workflow-task-validate', 'if(this.isWorkflowStepSession(s.execution.chatSessionId)){let p=', 'if(n.taskType&&!n.workflowPath?.startsWith("agent://")){let p="taskType is accepted only for an agent:// single-step workflow.";return r.recordToolError(new Error(p)),this.concludeToolUse(s,a,l(de.HandledError,p))}if(this.isWorkflowStepSession(s.execution.chatSessionId)){let p='],
 ['run-workflow-task-forward', 'QFi(n.workflowPath??"",n.inputs,s.execution.chatSessionId,n.runLabel)', 'QFi(n.workflowPath??"",n.inputs,s.execution.chatSessionId,n.runLabel,n.taskType)'],
 ['workflow-auto-inheritance', 'function P8(t,e){return Jve(t.modelId)??Jve(e.modelId)??Jve(e.parentModelId)}', 'function P8(t,e){return process.env.KIRO_PROXY_STAGE_BRIDGE==="1"&&(e.parentModelId==="auto"||e.modelId==="auto")?"auto":Jve(t.modelId)??Jve(e.modelId)??Jve(e.parentModelId)}'],
 ['main-execution-source', 'ye=this.controller.trigger(it,this.buildSessionServices(r,ne),j.signal),s()', 'ye=this.controller.trigger(it,this.buildSessionServices(r,ne),j.signal),kiroProxyStampMainExecutionV2(r,ye),s()'],
 ['step-meta-continue', 'let I={stepNode:n,userMessage:S,meta:{workflowId:e.workflowId,', 'let I={stepNode:n,userMessage:S,meta:{kiroProxyRevision:e.workflow.planRevision,kiroProxyDefinition:e.workflow,workflowId:e.workflowId,'],
 ['step-meta-create', 'm=n.prompt,g={stepNode:n,userMessage:wTt(E3u(m,h,(this.deps.generateFrameNonce??FVu)()),e.state.originalUserMessages??[]),meta:{workflowId:e.workflowId,', 'm=n.prompt,g={stepNode:n,userMessage:wTt(E3u(m,h,(this.deps.generateFrameNonce??FVu)()),e.state.originalUserMessages??[]),meta:{kiroProxyRevision:e.workflow.planRevision,kiroProxyDefinition:e.workflow,workflowId:e.workflowId,'],
 ['step-session-create', 'n.workflowId=t.meta.workflowId,n.workflowNodeId=t.meta.nodeId,n.checkpointWorkflow=', 'n.workflowId=t.meta.workflowId,n.workflowNodeId=t.meta.nodeId,process.env.KIRO_PROXY_STAGE_BRIDGE==="1"&&(n.kiroProxyRoute=kiroProxyStepRouteV2(t.meta,r,t.meta.kiroProxyRevision)),n.checkpointWorkflow='],
 ['step-session-resident-load', 'g=this.host.getSession(t);if(g){this.bindStepToRun(r.workflowId,g),this.restampLegacyStepSessionRoot(g,t,d);', 'g=this.host.getSession(t);if(g){this.bindStepToRun(r.workflowId,g),process.env.KIRO_PROXY_STAGE_BRIDGE==="1"&&(g.kiroProxyRoute=kiroProxyStepRouteV2(r,t,r.kiroProxyRevision)),this.restampLegacyStepSessionRoot(g,t,d);'],
 ['step-session-load', 'A.workflowId=r.workflowId,A.workflowNodeId=r.nodeId,this.restampLegacyStepSessionRoot(A,t,d)', 'A.workflowId=r.workflowId,A.workflowNodeId=r.nodeId,process.env.KIRO_PROXY_STAGE_BRIDGE==="1"&&(A.kiroProxyRoute=kiroProxyStepRouteV2(r,t,r.kiroProxyRevision)),this.restampLegacyStepSessionRoot(A,t,d)'],
 ['session-services', sessionAnchor, sessionAnchor.replace('return{workspace:', 'if(process.env.KIRO_PROXY_STAGE_BRIDGE==="1")e.kiroProxyRoute??=e.workflowId?{}:{taskType:"orchestration"};return{workspace:') + 'workflowId:e.workflowId,workflowNodeId:e.workflowNodeId,kiroProxyAutoDomain:process.env.KIRO_PROXY_STAGE_BRIDGE==="1"&&n==="auto",kiroProxyRoute:process.env.KIRO_PROXY_STAGE_BRIDGE==="1"&&n==="auto"&&!e.workflowId?{...e.kiroProxyRoute,executionId:void 0}:e.kiroProxyRoute,'],
 ['subagent-auto-inheritance', 'let y=t.inline?t.inline.resolvedModelId:yoe(e.model,"subagent-registered").modelId', 'let y=f.sessionServices.kiroProxyAutoDomain?"auto":t.inline?t.inline.resolvedModelId:yoe(e.model,"subagent-registered").modelId'],
 ['subagent-task-schema', 'S1i={name:Qe().min(1).describe(', 'S1i={taskType:Qe().min(1).max(64).regex(/^[a-z][a-z0-9_]*$/).optional().describe("Explicit duty for an independent sub-agent; omit to inherit the parent Auto context."+kiroProxyDutyHelpV2()),name:Qe().min(1).describe('],
 ['dag-task-schema', 'u={name:Qe().describe("Unique stage name"),role:l,', 'u={taskType:Qe().min(1).max(64).regex(/^[a-z][a-z0-9_]*$/).optional().describe("Explicit duty for this independent stage; omit to inherit the parent Auto context."+kiroProxyDutyHelpV2()),name:Qe().describe("Unique stage name"),role:l,'],
 ['dag-task-forward', 'input:{name:e.role,prompt:p,explanation:', 'input:{name:e.role,prompt:p,taskType:e.taskType,explanation:'],
 ['subagent-task-extract', 'inlineAgent:v,executionId:S}=e,E=I1i(w)', 'inlineAgent:v,executionId:S,taskType:kiroProxyTaskType}=e,E=I1i(w)'],
 ['subagent-task-forward', 'contextMessages:U,executionId:P,inline:j,events:', 'contextMessages:U,executionId:P,taskType:kiroProxyTaskType,inline:j,events:'],
 ['subagent-private-copy', 'sessionServices:f.sessionServices,promptContext:f.promptContext', 'sessionServices:process.env.KIRO_PROXY_STAGE_BRIDGE==="1"?{...f.sessionServices,kiroProxyRoute:t.taskType?{nodeId:t.executionId??"",definitionId:"dynamic://invoke-sub-agent",definitionRevision:"1",nodePath:"",taskType:t.taskType,executionId:t.executionId??"",workflowId:f.sessionServices.kiroProxyRoute?.workflowId??""}:{...f.sessionServices.kiroProxyRoute,executionId:f.sessionServices.kiroProxyRoute?.executionId??(f.subExecutionDepth===0?f.executionId:"")}}:f.sessionServices,promptContext:f.promptContext'],
 ['execution-call-options', optionsAnchor, optionsAnchor.replace('additionalModelRequestFields:', 'kiroProxyWorkflow:{...this.sessionServices.kiroProxyRoute,executionId:this.sessionServices.kiroProxyRoute?.executionId??(this.subExecutionDepth===0?this.executionId:""),autoDomain:this.sessionServices.kiroProxyAutoDomain},additionalModelRequestFields:')],
 ['sdk-generate-options', 'buildRequestCommand(l,s,e.additionalModelRequestFields,n,a)', 'buildRequestCommand(l,s,e.additionalModelRequestFields,n,a,e.kiroProxyWorkflow)'],
 ['sdk-stream-options', 'buildRequestCommand(F,t,e.additionalModelRequestFields,s,a)', 'buildRequestCommand(F,t,e.additionalModelRequestFields,s,a,e.kiroProxyWorkflow)'],
 ['sdk-command-header', 'buildRequestCommand(t,e,r,n,s){let a=new VUu({conversationState:e,profileArn:t.profileArn,additionalModelRequestFields:r,agentMode:this.agentMode,systemPrompt:n});return this.emitTestModelCallerHeaders&&s&&B6o(a,s),a}', 'buildRequestCommand(t,e,r,n,s,i){let a=new VUu({conversationState:e,profileArn:t.profileArn,additionalModelRequestFields:r,agentMode:this.agentMode,systemPrompt:n});return this.emitTestModelCallerHeaders&&s&&B6o(a,s),kiroProxyAttachStageV2(a,e,this.agentMode,i)}'],
 ['raw-body-open', rawStart, rawStart.replace('return ', 'return kiroProxyStageRawBodyV2((')],
 ['raw-body-close', rawEnd, '...Object.keys(p).length>0&&{metadata:p},...aFu(u)}),e,l,t.kiroProxyWorkflow)}function aFu(t)'],
 ['raw-workflow-option', 'getOrigin(),profileArn:me,agentMode:this.agentMode,additionalModelRequestFields:e.additionalModelRequestFields}', 'getOrigin(),profileArn:me,agentMode:this.agentMode,additionalModelRequestFields:e.additionalModelRequestFields,kiroProxyWorkflow:e.kiroProxyWorkflow}'],
 ['raw-header-option', 'userAgent:this.getUserAgent?.()});if(re.statusCode>=400)', 'userAgent:this.getUserAgent?.(),kiroProxyWorkflowHeaders:kiroProxyStageHeadersV2(t,this.agentMode,e.kiroProxyWorkflow)});if(re.statusCode>=400)'],
 ['raw-transport-header', '...e.userAgent?{"User-Agent":e.userAgent}:{}},p=XR(r);', '...e.userAgent?{"User-Agent":e.userAgent}:{},...e.kiroProxyWorkflowHeaders},p=XR(r);'],
]

export function applyAnchoredPatches(source) {
 if (source.includes('function kiroProxyStageEnvelopeV1(') || source.includes('function kiroProxyEnvelopeV2(')) throw new Error('Stage bridge is already present')
 for (const [name, before, after] of PATCHES) {
  const count = source.split(before).length - 1
  if (count !== 1) throw new Error(`Unique anchor check failed: ${name} (${count})`)
  source = source.replace(before, after)
 }
 new Script(source, { filename: 'kiro-stage-candidate.js' })
 return source
}

export function buildCandidate(bytes) {
 if (sha256(bytes) !== ORIGINAL_SHA256) throw new Error('Unsupported Kiro bundle SHA256; no guessed or partial patch is allowed')
 return applyAnchoredPatches(bytes.toString('utf8'))
}

export function buildPatchManifest(bytes) {
 const candidate = buildCandidate(bytes)
 return {
  original_sha256: sha256(bytes),
  candidate_sha256: sha256(candidate),
  patches: PATCHES.map(([name, before, after]) => ({name, before, after})),
 }
}

function main() {
 const [input, output, ...extra] = process.argv.slice(2)
 if (!input || extra.length || input.startsWith('--') || output?.startsWith('--')) throw new Error('Usage: node tools/kiro_stage_bridge.mjs <original-extension.js> [workspace-candidate.js]')
 const originalPath = resolve(input)
 const bytes = readFileSync(originalPath)
 const candidate = buildCandidate(bytes)
 const manifest = buildPatchManifest(bytes)
 const report = {original_sha256:manifest.original_sha256,candidate_sha256:manifest.candidate_sha256,patches:PATCHES.map(([name])=>name),bridge_env:BRIDGE_ENV,status:'candidate checked; installation unchanged'}
 if (output) {
  const outputPath = resolve(output)
  const workspace = resolve(dirname(fileURLToPath(import.meta.url)), '..')
  if (!outputPath.toLowerCase().startsWith((workspace + '/').replaceAll('/', '\\').toLowerCase()) && !outputPath.startsWith(workspace + '/')) throw new Error('Candidate must remain inside this workspace')
  if (outputPath.toLowerCase() === originalPath.toLowerCase() || existsSync(outputPath) || existsSync(outputPath + '.manifest.json')) throw new Error('Refusing to overwrite an existing file')
  mkdirSync(dirname(outputPath), {recursive:true})
  writeFileSync(outputPath, candidate, {flag:'wx'})
  writeFileSync(outputPath + '.manifest.json', JSON.stringify(manifest,null,2)+'\n', {flag:'wx'})
  report.candidate_path = outputPath
 }
 process.stdout.write(JSON.stringify(report,null,2)+'\n')
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
 try { main() } catch (error) { process.stderr.write(error.message+'\n'); process.exitCode=1 }
}
