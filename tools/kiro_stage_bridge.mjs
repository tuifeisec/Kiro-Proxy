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
function kiroProxyStageEnvelopeV1(state,mode,workflow){
 if(process.env.KIRO_PROXY_STAGE_BRIDGE!=="1"||!workflow||!workflow.workflowId||!workflow.nodeId)return;
 let stage=String(workflow.nodeId).trim().toLowerCase();
 if(!/^[a-z0-9._:/-]{1,128}$/.test(stage))throw new Error("Kiro proxy workflow nodeId is invalid");
 if(typeof state.conversationId!=="string"||!state.conversationId.trim())throw new Error("Kiro proxy workflow requires a request conversationId");
 let continuation=state.agentContinuationId??"";
 if(typeof continuation!=="string")throw new Error("Kiro proxy continuation identity must be a string");
 return{version:1,stage:stage,workflowId:String(workflow.workflowId),conversationId:state.conversationId,agentMode:typeof mode==="string"?mode:"",agentContinuationId:continuation};
}
function kiroProxyStageHeadersV1(state,mode,workflow){
 let meta=kiroProxyStageEnvelopeV1(state,mode,workflow);
 return meta?{"X-Kiro-Proxy-Workflow":JSON.stringify(meta)}:{};
}
function kiroProxyAttachStageV1(command,state,mode,workflow){
 let headers=kiroProxyStageHeadersV1(state,mode,workflow);
 if(Object.keys(headers).length)command.middlewareStack.add(next=>async args=>{args.request.headers={...args.request.headers,...headers};return next(args)},{step:"finalizeRequest",priority:"low",name:"kiroProxyStageV1"});
 return command;
}
function kiroProxyStageRawBodyV1(body,state,mode,workflow){
 let meta=kiroProxyStageEnvelopeV1(state,mode,workflow);
 if(!meta)return body;
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
 ['session-services', sessionAnchor, sessionAnchor + 'workflowId:e.workflowId,workflowNodeId:e.workflowNodeId,'],
 ['execution-call-options', optionsAnchor, optionsAnchor.replace('additionalModelRequestFields:', 'kiroProxyWorkflow:{workflowId:this.sessionServices.workflowId,nodeId:this.sessionServices.workflowNodeId},additionalModelRequestFields:')],
 ['sdk-generate-options', 'buildRequestCommand(l,s,e.additionalModelRequestFields,n,a)', 'buildRequestCommand(l,s,e.additionalModelRequestFields,n,a,e.kiroProxyWorkflow)'],
 ['sdk-stream-options', 'buildRequestCommand(F,t,e.additionalModelRequestFields,s,a)', 'buildRequestCommand(F,t,e.additionalModelRequestFields,s,a,e.kiroProxyWorkflow)'],
 ['sdk-command-header', 'buildRequestCommand(t,e,r,n,s){let a=new VUu({conversationState:e,profileArn:t.profileArn,additionalModelRequestFields:r,agentMode:this.agentMode,systemPrompt:n});return this.emitTestModelCallerHeaders&&s&&B6o(a,s),a}', 'buildRequestCommand(t,e,r,n,s,i){let a=new VUu({conversationState:e,profileArn:t.profileArn,additionalModelRequestFields:r,agentMode:this.agentMode,systemPrompt:n});return this.emitTestModelCallerHeaders&&s&&B6o(a,s),kiroProxyAttachStageV1(a,e,this.agentMode,i)}'],
 ['raw-body-open', rawStart, rawStart.replace('return ', 'return kiroProxyStageRawBodyV1((')],
 ['raw-body-close', rawEnd, '...Object.keys(p).length>0&&{metadata:p},...aFu(u)}),e,l,t.kiroProxyWorkflow)}function aFu(t)'],
 ['raw-workflow-option', 'getOrigin(),profileArn:me,agentMode:this.agentMode,additionalModelRequestFields:e.additionalModelRequestFields}', 'getOrigin(),profileArn:me,agentMode:this.agentMode,additionalModelRequestFields:e.additionalModelRequestFields,kiroProxyWorkflow:e.kiroProxyWorkflow}'],
 ['raw-header-option', 'userAgent:this.getUserAgent?.()});if(re.statusCode>=400)', 'userAgent:this.getUserAgent?.(),kiroProxyWorkflowHeaders:kiroProxyStageHeadersV1(t,this.agentMode,e.kiroProxyWorkflow)});if(re.statusCode>=400)'],
 ['raw-transport-header', '...e.userAgent?{"User-Agent":e.userAgent}:{}},p=XR(r);', '...e.userAgent?{"User-Agent":e.userAgent}:{},...e.kiroProxyWorkflowHeaders},p=XR(r);'],
]

export function applyAnchoredPatches(source) {
 if (source.includes('function kiroProxyStageEnvelopeV1(')) throw new Error('Stage bridge is already present')
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

function main() {
 const [input, output, ...extra] = process.argv.slice(2)
 if (!input || extra.length || input.startsWith('--') || output?.startsWith('--')) throw new Error('Usage: node tools/kiro_stage_bridge.mjs <original-extension.js> [workspace-candidate.js]')
 const originalPath = resolve(input)
 const bytes = readFileSync(originalPath)
 const candidate = buildCandidate(bytes)
 const report = {original_sha256:sha256(bytes),candidate_sha256:sha256(candidate),patches:PATCHES.map(([name])=>name),bridge_env:BRIDGE_ENV,status:'candidate checked; installation unchanged'}
 if (output) {
  const outputPath = resolve(output)
  const workspace = resolve(dirname(fileURLToPath(import.meta.url)), '..')
  if (!outputPath.toLowerCase().startsWith((workspace + '/').replaceAll('/', '\\').toLowerCase()) && !outputPath.startsWith(workspace + '/')) throw new Error('Candidate must remain inside this workspace')
  if (outputPath.toLowerCase() === originalPath.toLowerCase() || existsSync(outputPath) || existsSync(outputPath + '.manifest.json')) throw new Error('Refusing to overwrite an existing file')
  mkdirSync(dirname(outputPath), {recursive:true})
  writeFileSync(outputPath, candidate, {flag:'wx'})
  writeFileSync(outputPath + '.manifest.json', JSON.stringify(report,null,2)+'\n', {flag:'wx'})
  report.candidate_path = outputPath
 }
 process.stdout.write(JSON.stringify(report,null,2)+'\n')
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
 try { main() } catch (error) { process.stderr.write(error.message+'\n'); process.exitCode=1 }
}
