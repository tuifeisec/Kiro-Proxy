import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { Script, createContext } from 'node:vm'
import { BRIDGE_SOURCE, ORIGINAL_SHA256, PATCHES, buildCandidate, sha256 } from '../tools/kiro_stage_bridge.mjs'

function runtime(enabled = '1') {
 const context = createContext({process:{env:{KIRO_PROXY_STAGE_BRIDGE:enabled}}})
 new Script(BRIDGE_SOURCE).runInContext(context)
 return context
}
const state = {conversationId:'wire-uuid',agentContinuationId:'branch-1',currentMessage:{userInputMessage:{modelId:'auto',userInputMessageContext:{toolResults:[{toolUseId:'call-1'}]}}}}
const workflow = {workflowId:'wf-1',nodeId:'plan'}

test('synchronous envelope binds actual wire identity, not step session', () => {
 const context = runtime()
 for (const nodeId of ['run','plan','implement','review','finalize','investigate']) {
  const meta = context.kiroProxyStageEnvelopeV1(state,'wf-coder',{...workflow,nodeId})
  assert.equal(meta.stage,nodeId)
  assert.equal(meta.conversationId,'wire-uuid')
  assert.equal(meta.agentContinuationId,'branch-1')
 }
 assert.equal(context.kiroProxyStageEnvelopeV1(state,'wf-coder',{}),undefined)
 assert.equal(runtime('0').kiroProxyStageEnvelopeV1(state,'wf-coder',workflow),undefined)
 assert.throws(()=>context.kiroProxyStageEnvelopeV1({...state,conversationId:''},'vibe',workflow))
})

test('SDK middleware and raw body/header use the same synchronous metadata', async () => {
 const context = runtime()
 let middleware, config
 const command = {middlewareStack:{add:(fn,options)=>{middleware=fn;config=options}}}
 context.kiroProxyAttachStageV1(command,state,'wf-coder',workflow)
 assert.equal(config.step,'finalizeRequest')
 const sdk = await middleware(async args=>args)({request:{headers:{Authorization:'Bearer mock'}}})
 const rawHeaders = context.kiroProxyStageHeadersV1(state,'wf-coder',workflow)
 assert.equal(sdk.request.headers['X-Kiro-Proxy-Workflow'],rawHeaders['X-Kiro-Proxy-Workflow'])
 assert.equal(sdk.request.headers.Authorization,'Bearer mock')
 const original = {model:'auto',input:[{type:'function_call_output',call_id:'call-1',output:'done'}],metadata:{'kiro:conversation_id':'wire-uuid'}}
 const body = context.kiroProxyStageRawBodyV1(original,state,'wf-coder',workflow)
 assert.equal(body.metadata['kiro:proxy_current_tool_results'],1)
 assert.equal(body.metadata['kiro:proxy_continuation_id'],'branch-1')
 assert.deepEqual(body.input,original.input)
 assert.equal(original.metadata['kiro:proxy_current_tool_results'],undefined)
 const newTask = {...state,currentMessage:{userInputMessage:{}}}
 assert.equal(context.kiroProxyStageRawBodyV1(original,newTask,'wf-coder',workflow).metadata['kiro:proxy_current_tool_results'],0)
 assert.equal(runtime('0').kiroProxyStageRawBodyV1(original,state,'wf-coder',workflow),original)
})

test('exact installed source is syntax checked and both transport call sites are patched', () => {
 const installed = process.env.KIRO_STAGE_TEST_BUNDLE
 if (!installed) return
 const bytes = readFileSync(installed)
 assert.equal(sha256(bytes),ORIGINAL_SHA256)
 const candidate = buildCandidate(bytes)
 for (const [name,,after] of PATCHES) assert.ok(candidate.includes(after),name)
 assert.throws(()=>buildCandidate(Buffer.from(candidate)),/Unsupported/)
 assert.throws(()=>buildCandidate(Buffer.from('different build')),/Unsupported/)
 // 执行真实 session services 构造和 modelStream2 的 options 表达式，核对阶段源。
 const servicesContext = runtime()
 Object.assign(servicesContext,{Vc:'mock-provider',d4d:()=>64000,d5r:()=>undefined,H5:()=>[],bis:()=>undefined,c:value=>value})
 const servicesStart = candidate.indexOf('buildSessionServices(e,r){')
 const servicesEnd = candidate.indexOf('createDefinitionForMode(',servicesStart)
 new Script('class ServicesFixture{requireSessionModelId(){return "auto"};'+candidate.slice(servicesStart,servicesEnd)+'};globalThis.servicesFixture=new ServicesFixture').runInContext(servicesContext)
 const session = {policySession:{authority:{requireAvailable(){}}},workflowId:'wf-actual',workflowNodeId:'review',workspace:{name:'mock'}}
 const services = servicesContext.servicesFixture.buildSessionServices(session)
 assert.equal(services.workflowId,session.workflowId)
 assert.equal(services.workflowNodeId,session.workflowNodeId)
 const optionsStart = candidate.indexOf('k={signal:A.signal,onRecoveryFired:_')+2
 const optionsEnd = candidate.indexOf(';l();let P=await g.stream(d,k)',optionsStart)
 assert.ok(optionsStart>1 && optionsEnd>optionsStart)
 Object.assign(servicesContext,{A:{signal:'mock-signal'},_:()=>{},C:()=>{},I:()=>{},h:'chat',f:{streamAttemptConsumer:{mock:true}},PAt:value=>value})
 servicesContext.execution = {sessionServices:services,model:{},chatSessionId:'step-session'}
 const actualOptions = new Script('(function(){return '+candidate.slice(optionsStart,optionsEnd)+'}).call(execution)').runInContext(servicesContext)
 assert.equal(actualOptions.kiroProxyWorkflow.workflowId,'wf-actual')
 assert.equal(actualOptions.kiroProxyWorkflow.nodeId,'review')
 assert.equal(actualOptions.streamAttemptConsumer.mock,true)
 assert.equal(actualOptions.metadata.sessionId,'step-session')
 // 从实际候选中执行 SDK command 和 raw request adapter，transport 本身使用 Mock。
 const context = runtime()
 context.VUu = class { constructor(input){this.input=input;this.middlewareStack={add:(fn)=>{this.middleware=fn}}} }
 context.B6o = ()=>{throw new Error('test-only middleware must stay disabled')}
 context.iFu = ()=>[{type:'function',name:'tool'}]
 context.oFu = ()=>[{type:'function_call_output',call_id:'call-1',output:'done'}]
 context.aFu = ()=>({})
 const commandStart = candidate.indexOf('buildRequestCommand(t,e,r,n,s,i){')
 const commandEnd = candidate.indexOf('extractSystemPrompt(t){',commandStart)
 new Script('class BridgeFixture{agentMode="wf-coder";emitTestModelCallerHeaders=false;'+candidate.slice(commandStart,commandEnd)+'};globalThis.fixture=new BridgeFixture').runInContext(context)
 const command = context.fixture.buildRequestCommand({},state,undefined,'policy',undefined,workflow)
 assert.ok(command.middleware)
 const start = candidate.indexOf('function sFu(t){')
 const end = candidate.indexOf('function aFu(t)',start)
 new Script(candidate.slice(start,end)).runInContext(context)
 const raw = context.sFu({conversationState:state,agentMode:'wf-coder',modelId:'auto',kiroProxyWorkflow:workflow})
 assert.equal(raw.metadata['kiro:proxy_current_tool_results'],1)
 assert.equal(raw.metadata['kiro:conversation_id'],'wire-uuid')
 context.Bs = value=>value
 const splitStart = candidate.indexOf('_separateRunnableConfigFromCallOptions(t){')
 const splitEnd = candidate.indexOf('async _callWithConfig(',splitStart)
 new Script('class SplitFixture{'+candidate.slice(splitStart,splitEnd)+'};globalThis.splitFixture=new SplitFixture').runInContext(context)
 const [runnable,call] = context.splitFixture._separateRunnableConfigFromCallOptions({metadata:{sessionId:'step-session'},kiroProxyWorkflow:workflow,streamAttemptConsumer:{}})
 assert.equal(call.metadata,undefined)
 assert.equal(runnable.metadata.sessionId,'step-session')
 assert.deepEqual(call.kiroProxyWorkflow,workflow)
})

test('actual raw transport emits bound private header and body with HTTP Mock', async () => {
 const installed = process.env.KIRO_STAGE_TEST_BUNDLE
 if (!installed) return
 const candidate = buildCandidate(readFileSync(installed))
 const context = runtime()
 let captured
 context.URL = URL
 context.Buffer = Buffer
 context.XR = ()=>undefined
 context.T = {info(){},warn(){}}
 context.LMi = 'retry-after'
 context.OMi = {request:(url,options,callback)=>{
  const request = {setTimeout(){},on(){return this},once(){return this},write(body){captured={url:String(url),options,body}},end(){callback({statusCode:200,headers:{'x-request-id':'mock'},async *[Symbol.asyncIterator](){yield Buffer.from('data: {}\n\n')}})}}
  return request
 }}
 context.MMi = context.OMi
 const start = candidate.indexOf('async function uFu(t,e){')
 const end = candidate.indexOf('async function dFu(t,e){',start)
 new Script(candidate.slice(start,end)).runInContext(context)
 const headers = context.kiroProxyStageHeadersV1(state,'wf-coder',workflow)
 const response = await context.uFu({model:'auto',input:[]},{endpoint:'https://runtime.test',token:'mock',origin:'AI_EDITOR',kiroProxyWorkflowHeaders:headers})
 assert.equal(response.statusCode,200)
 assert.equal(captured.url,'https://runtime.test/v1/responses')
 assert.equal(captured.options.headers['X-Kiro-Proxy-Workflow'],headers['X-Kiro-Proxy-Workflow'])
 assert.equal(captured.options.headers.Authorization,'Bearer mock')
 assert.equal(JSON.parse(captured.body).model,'auto')
})
