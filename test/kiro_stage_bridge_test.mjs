import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { Script, createContext } from 'node:vm'
import { createRequire } from 'node:module'
import { BRIDGE_SOURCE, ORIGINAL_SHA256, PATCHES, buildCandidate, buildPatchManifest, sha256 } from '../tools/kiro_stage_bridge.mjs'

function runtime(enabled = '1') {
 const context = createContext({Buffer,require:createRequire(import.meta.url),hoe:()=>[],process:{env:{KIRO_PROXY_STAGE_BRIDGE:enabled}}})
 new Script(BRIDGE_SOURCE).runInContext(context)
 return context
}
const state = {conversationId:'wire-uuid',agentContinuationId:'branch-1',currentMessage:{userInputMessage:{modelId:'auto',userInputMessageContext:{toolResults:[{toolUseId:'call-1'}]}}}}
const workflow = {workflowId:'wf-1',nodeId:'plan',definitionId:'bundled://feature-pipeline',definitionRevision:ORIGINAL_SHA256+':0',nodePath:'plan',taskType:'implementation_plan',executionId:'step-session',autoDomain:true}

test('trusted recipe source and complete path select a declared duty', () => {
 const context = runtime()
 context.kiroProxyRecordRunV2('wf-1',{definitionId:'bundled://feature-pipeline',bundleSha:ORIGINAL_SHA256})
 const definition = {steps:[{type:'repeat',id:'code-loop',steps:[{type:'parallel',id:'code-reviews',branches:[{type:'step',id:'review-gpt'}]}]}]}
 const meta = {workflowId:'wf-1',nodeId:'review-gpt',nodePath:['wf-1','code-loop','iter-0','code-reviews','review-gpt'],kiroProxyDefinition:definition}
 const route = context.kiroProxyStepRouteV2(meta,'step-42',0)
 assert.equal(route.nodePath,'code-loop/code-reviews/review-gpt')
 assert.equal(route.taskType,'code_review')
 assert.equal(route.executionId,'step-42')
 assert.equal(route.definitionRevision,ORIGINAL_SHA256+':0')
 assert.equal(context.kiroProxyStepRouteV2(meta,'step-42',1).taskType,'')
 const unknown = context.kiroProxyStepRouteV2({...meta,workflowId:'unknown'},'step-43',0)
 assert.match(unknown.definitionId,/^content:\/\/[a-f0-9]{64}$/)
 assert.equal(unknown.taskType,'')
 assert.equal(context.kiroProxyStepRouteV2({...meta,nodePath:['wf-1','elsewhere','review-gpt']},'step-44',0).taskType,'')
})

test('definition traversal preserves real iter IDs and encodes structural names without collisions', () => {
 const context = runtime()
 const definition = {steps:[
  {type:'sequence',id:'phase',steps:[{type:'step',id:'iter-0'},{type:'step',id:'iter-1'}]},
  {type:'repeat',id:'loop',steps:[{type:'step',id:'iter-0'}]},
  {type:'parallel',id:'fork',branches:[{type:'step',id:'br/anch'},{type:'step',id:'br%2Fanch'},{type:'step',id:''},{type:'step',id:'π'}]},
  {type:'step',id:'wf-1'},
 ]}
 const path = (parts,nodeId,source=definition)=>context.kiroProxyNodePathV2(['wf-1',...parts],'wf-1',source,nodeId)
 assert.equal(path(['phase','iter-0'],'iter-0'),'phase/iter-0')
 assert.equal(path(['phase','iter-1'],'iter-1'),'phase/iter-1')
 assert.equal(path(['loop','iter-3','iter-0'],'iter-0'),'loop/iter-0')
 assert.equal(path(['fork','br/anch'],'br/anch'),'fork/br%2Fanch')
 assert.equal(path(['fork','br%2Fanch'],'br%2Fanch'),'fork/br%252Fanch')
 assert.equal(path(['fork',''],''),'fork/%')
 assert.equal(path(['fork','π'],'π'),'fork/%CF%80')
 assert.equal(path(['wf-1'],'wf-1'),'wf-1')
 assert.equal(path(['phase','iter-0'],'iter-0',null),'')
 assert.equal(path(['phase','iter-0'],'iter-1'),'')
 assert.equal(path(['loop','iter-0'],'iter-0'),'')
 assert.equal(path(['phase','iter-0'],'iter-0',{steps:[{type:'step',id:'phase'},{type:'step',id:'phase'}]}),'')
})

test('all seven inventoried bundled recipes retain their declared step paths', () => {
 const inventory = JSON.parse(readFileSync(new URL('../docs/2026-10-06-kiro-workflow-node-inventory.json',import.meta.url),'utf8'))
 assert.equal(inventory.builtin.recipes.length,7)
 const context = runtime()
 let checked = 0
 for (const recipe of inventory.builtin.recipes) {
  const nodes = new Map(recipe.nodes.map(entry=>[entry.id,{type:entry.type,id:entry.id,...entry.type==='parallel'?{branches:[]}:entry.type==='repeat'||entry.type==='sequence'?{steps:[]}:{}}]))
  const definition = {steps:[]}
  for (const entry of recipe.nodes) {
   const parent = entry.parentNodeId===null?definition:nodes.get(entry.parentNodeId)
   assert.ok(parent,recipe.name+'/'+entry.id)
   const children = parent.type==='parallel'?parent.branches:parent.steps
   children.push(nodes.get(entry.id))
  }
  const workflowId = 'wf-'+recipe.name
  context.kiroProxyRecordRunV2(workflowId,{definitionId:'bundled://'+recipe.name,bundleSha:ORIGINAL_SHA256})
  for (const entry of recipe.nodes.filter(node=>node.type==='step')) {
   const ancestors = []
   let current = entry
   while (current.parentNodeId!==null) {
    current = recipe.nodes.find(node=>node.id===current.parentNodeId)
    assert.ok(current,recipe.name+'/'+entry.id)
    ancestors.unshift(current)
   }
   const rawPath = [workflowId]
   for (const ancestor of ancestors) {
    rawPath.push(ancestor.id)
    if (ancestor.type==='repeat') rawPath.push('iter-0')
   }
   rawPath.push(entry.id)
   const route = context.kiroProxyStepRouteV2({workflowId,nodeId:entry.id,nodePath:rawPath,kiroProxyDefinition:definition},'step-'+checked,0)
   assert.equal(route.nodePath,[...ancestors.map(node=>node.id),entry.id].join('/'),recipe.name+'/'+entry.id)
   assert.ok(route.taskType,recipe.name+'/'+entry.id)
   checked++
  }
 }
 assert.equal(checked,20)
})

test('persisted definition restores exact builtin or content identity without guessing synthetic run', () => {
 const context = runtime()
 const recipe = {name:'feature-pipeline',steps:[{type:'step',id:'plan',agent:'wf-planner',prompt:'fixed'}]}
 context.hoe = () => [{name:'feature-pipeline',workflow:recipe}]
 context.require = createRequire(import.meta.url)
 const recovered = context.kiroProxyDefinitionV2({...recipe,planRevision:0})
 assert.equal(recovered.definitionId,'bundled://feature-pipeline')
 const changed = context.kiroProxyDefinitionV2({...recipe,steps:[{...recipe.steps[0],prompt:'changed'}],planRevision:0})
 assert.match(changed.definitionId,/^content:\/\/[a-f0-9]{64}$/)
 assert.equal(context.kiroProxyDefinitionV2({...recipe,steps:[{...recipe.steps[0],prompt:'changed'}],planRevision:2}).definitionId,changed.definitionId)
 assert.equal(context.kiroProxyDefinitionV2({name:'agent-wf-planner',steps:[{type:'step',id:'run',agent:'wf-planner'}],planRevision:0}),undefined)
})

test('private workflow source sidecar survives a fresh bridge context', async () => {
 const files = new Map()
 const fs = {
  async writeFile(path,value){files.set(path,value)},
  async rename(from,to){files.set(to,files.get(from));files.delete(from)},
  async readFile(path){if(!files.has(path)){const error = new Error('missing');error.code='ENOENT';throw error}return files.get(path)},
  async unlink(path){files.delete(path)},
 }
 const setup = () => {
  const context = runtime()
  context.Mn = {join:(...parts)=>parts.join('/')}
  context.fH = (base,id)=>base+'/'+id
  context.Ix = {randomUUID:()=> 'mock-id'}
  return context
 }
 const writer = setup()
 writer.require = createRequire(import.meta.url)
 const synthetic = {name:'agent-wf-planner',steps:[{type:'step',id:'run',agent:'wf-planner',prompt:'{{prompt}}'}]}
 const investigationRevision = writer.kiroProxyAgentRevisionV2(synthetic,'investigation')
 assert.notEqual(investigationRevision,writer.kiroProxyAgentRevisionV2(synthetic,'design'))
 const source = {definitionId:'agent://wf-planner',definitionRevision:investigationRevision,taskType:'investigation',parentRoute:{taskType:'orchestration',executionId:'parent-1'}}
 await writer.kiroProxyWriteSourceV2(fs,'/runs','wf-1',source)
 assert.ok(files.has('/runs/wf-1/kiro-proxy-routing-v2.json'))
 const reader = setup()
 reader.require = createRequire(import.meta.url)
 reader.hoe = () => []
 const restored = await reader.kiroProxyReadSourceV2(fs,'/runs','wf-1')
 assert.equal(restored.definitionId,'agent://wf-planner')
 assert.equal(restored.definitionRevision,investigationRevision)
 assert.equal(restored.taskType,'investigation')
 assert.equal(restored.parentRoute.executionId,'parent-1')
 const independent = reader.kiroProxyStepRouteV2({workflowId:'wf-1',nodeId:'run',nodePath:['wf-1','run'],kiroProxyDefinition:{...synthetic,planRevision:0}},'step-1',0)
 assert.equal(independent.taskType,'investigation')
 assert.equal(independent.executionId,'step-1')
 assert.equal(independent.definitionRevision,investigationRevision)
 const revised = {...synthetic,steps:[{...synthetic.steps[0],prompt:'revised'}],planRevision:1}
 const revisedRoute = reader.kiroProxyStepRouteV2({workflowId:'wf-1',nodeId:'run',nodePath:['wf-1','run'],kiroProxyDefinition:revised},'step-1',1)
 assert.equal(revisedRoute.taskType,'investigation')
 assert.equal(revisedRoute.definitionRevision,reader.kiroProxyAgentRevisionV2(revised,'investigation'))
 assert.notEqual(revisedRoute.definitionRevision,investigationRevision)
 const expanded = {...revised,steps:[...revised.steps,{type:'step',id:'other',agent:'wf-coder',prompt:'new'}]}
 const expandedRoute = reader.kiroProxyStepRouteV2({workflowId:'wf-1',nodeId:'run',nodePath:['wf-1','run'],kiroProxyDefinition:expanded},'step-1',2)
 assert.equal(expandedRoute.taskType,'')
 assert.match(expandedRoute.definitionId,/^content:\/\/[a-f0-9]{64}$/)
 const differentId = {...revised,steps:[{...revised.steps[0],id:'other'}]}
 assert.equal(reader.kiroProxyStepRouteV2({workflowId:'wf-1',nodeId:'other',nodePath:['wf-1','other'],kiroProxyDefinition:differentId},'step-1',2).taskType,'')
 const container = {...revised,steps:[{type:'repeat',id:'run',steps:revised.steps}]}
 assert.equal(reader.kiroProxyStepRouteV2({workflowId:'wf-1',nodeId:'run',nodePath:['wf-1','run'],kiroProxyDefinition:container},'step-1',2).taskType,'')
 await writer.kiroProxyWriteSourceV2(fs,'/runs','wf-2',{definitionId:'',taskType:'',parentRoute:{nodeId:'root',definitionId:'parent://definition',definitionRevision:'7',nodePath:'root',taskType:'orchestration',executionId:'parent-1',workflowId:''}})
 await reader.kiroProxyReadSourceV2(fs,'/runs','wf-2')
 const inherited = reader.kiroProxyStepRouteV2({workflowId:'wf-2',nodeId:'run',nodePath:['wf-2','run'],kiroProxyDefinition:{...synthetic,planRevision:0}},'step-2',0)
 assert.equal(inherited.definitionId,'parent://definition')
 assert.equal(inherited.taskType,'orchestration')
 assert.equal(inherited.executionId,'parent-1')
 assert.equal(inherited.workflowId,'wf-2')
 const inheritedRevised = reader.kiroProxyStepRouteV2({workflowId:'wf-2',nodeId:'run',nodePath:['wf-2','run'],kiroProxyDefinition:revised},'step-2',1)
 assert.equal(inheritedRevised.definitionId,'parent://definition')
 assert.equal(inheritedRevised.executionId,'parent-1')
 assert.equal(inheritedRevised.taskType,'orchestration')
 const longSource = {definitionId:'d'.repeat(300),taskType:'investigation',parentRoute:{definitionId:'d'.repeat(900),nodePath:'n'.repeat(900)}}
 await writer.kiroProxyWriteSourceV2(fs,'/runs','wf-long',longSource)
 assert.equal((await reader.kiroProxyReadSourceV2(fs,'/runs','wf-long')).parentRoute.nodePath.length,900)
 await assert.rejects(writer.kiroProxyWriteSourceV2(fs,'/runs','wf-too-long',{...longSource,definitionId:'d'.repeat(1025)}),/workflow source definitionId is invalid/)
 await assert.rejects(writer.kiroProxyWriteSourceV2(fs,'/runs','wf-whitespace',{...longSource,taskType:' investigation'}),/workflow source taskType is invalid/)
 assert.equal(await reader.kiroProxyReadSourceV2(fs,'/runs','historical'),undefined)
 files.set('/runs/wf-1/kiro-proxy-routing-v2.json','{"version":2,"workflowId":"other"}')
 await assert.rejects(reader.kiroProxyReadSourceV2(fs,'/runs','wf-1'),/invalid version or workflowId/)
 await assert.rejects(writer.kiroProxyWriteSourceV2({...fs,async writeFile(){throw new Error('mock disk full')}},'/runs','wf-3',source),/mock disk full/)
 await runtime('0').kiroProxyWriteSourceV2({...fs,async writeFile(){throw new Error('disabled bridge wrote sidecar')}},'/runs','wf-disabled',source)
 assert.equal(files.has('/runs/wf-disabled/kiro-proxy-routing-v2.json'),false)
})

test('synchronous envelope binds actual wire identity, not step session', () => {
 const context = runtime()
 for (const nodeId of ['run','plan','implement','review','finalize','investigate']) {
  const meta = context.kiroProxyEnvelopeV2(state,'wf-coder',{...workflow,nodeId})
  assert.equal(meta.version,2)
  assert.equal(meta.nodeId,nodeId)
  assert.equal(meta.conversationId,'wire-uuid')
  assert.equal(meta.agentContinuationId,'branch-1')
 }
 const root = context.kiroProxyEnvelopeV2(state,'vibe',{autoDomain:true,taskType:'orchestration'})
 assert.equal(root.nodeId,'')
 assert.equal(root.workflowId,'')
 assert.equal(root.taskType,'orchestration')
 assert.equal(context.kiroProxyEnvelopeV2(state,'wf-coder',{}),undefined)
 assert.equal(context.kiroProxyEnvelopeV2(state,'wf-coder',{...workflow,autoDomain:false}),undefined)
 assert.equal(runtime('0').kiroProxyEnvelopeV2(state,'wf-coder',workflow),undefined)
 assert.throws(()=>context.kiroProxyEnvelopeV2({...state,conversationId:''},'vibe',workflow))
 assert.throws(()=>context.kiroProxyEnvelopeV2({...state,currentMessage:{userInputMessage:{modelId:'group-1'}}},'vibe',workflow),/Auto request model/)
 assert.throws(()=>context.kiroProxyEnvelopeV2(state,'vibe',{...workflow,nodeId:42}),/workflow nodeId is invalid/)
})

test('SDK middleware and raw body/header use the same synchronous metadata', async () => {
 const context = runtime()
 let middleware, config
 const command = {middlewareStack:{add:(fn,options)=>{middleware=fn;config=options}}}
 context.kiroProxyAttachStageV2(command,state,'wf-coder',workflow)
 assert.equal(config.step,'finalizeRequest')
 const sdk = await middleware(async args=>args)({request:{headers:{Authorization:'Bearer mock'}}})
 const rawHeaders = context.kiroProxyStageHeadersV2(state,'wf-coder',workflow)
 assert.equal(sdk.request.headers['X-Kiro-Proxy-Workflow'],rawHeaders['X-Kiro-Proxy-Workflow'])
 assert.equal(sdk.request.headers.Authorization,'Bearer mock')
 const original = {model:'auto',input:[{type:'function_call_output',call_id:'call-1',output:'done'}],metadata:{'kiro:conversation_id':'wire-uuid'}}
 const body = context.kiroProxyStageRawBodyV2(original,state,'wf-coder',workflow)
 assert.equal(body.metadata['kiro:proxy_current_tool_results'],1)
 assert.equal(body.metadata['kiro:proxy_continuation_id'],'branch-1')
 assert.deepEqual(body.input,original.input)
 assert.equal(original.metadata['kiro:proxy_current_tool_results'],undefined)
 const newTask = {...state,currentMessage:{userInputMessage:{modelId:'auto'}}}
 assert.equal(context.kiroProxyStageRawBodyV2(original,newTask,'wf-coder',workflow).metadata['kiro:proxy_current_tool_results'],0)
 assert.equal(runtime('0').kiroProxyStageRawBodyV2(original,state,'wf-coder',workflow),original)
 assert.throws(()=>context.kiroProxyStageRawBodyV2({...original,model:'group-1'},state,'wf-coder',workflow),/Auto raw model/)
})

test('exact installed source is syntax checked and both transport call sites are patched', () => {
 const installed = process.env.KIRO_STAGE_TEST_BUNDLE
 if (!installed) return
 const bytes = readFileSync(installed)
 assert.equal(sha256(bytes),ORIGINAL_SHA256)
 const candidate = buildCandidate(bytes)
 const manifest = buildPatchManifest(bytes)
 assert.equal(manifest.original_sha256,ORIGINAL_SHA256)
 assert.equal(manifest.candidate_sha256,sha256(candidate))
 assert.deepEqual(manifest.patches.map(patch=>patch.name),PATCHES.map(([name])=>name))
 for (const [name,,after] of PATCHES) assert.ok(candidate.includes(after),name)
 assert.throws(()=>buildCandidate(Buffer.from(candidate)),/Unsupported/)
 assert.throws(()=>buildCandidate(Buffer.from('different build')),/Unsupported/)
 // 执行真实 session services 构造和 modelStream2 的 options 表达式，核对阶段源。
 const servicesContext = runtime()
 Object.assign(servicesContext,{Vc:'mock-provider',d4d:()=>64000,d5r:()=>undefined,H5:()=>[],bis:()=>undefined,c:value=>value})
 const servicesStart = candidate.indexOf('buildSessionServices(e,r){')
 const servicesEnd = candidate.indexOf('createDefinitionForMode(',servicesStart)
 new Script('class ServicesFixture{requireSessionModelId(){return "auto"};'+candidate.slice(servicesStart,servicesEnd)+'};globalThis.servicesFixture=new ServicesFixture').runInContext(servicesContext)
 const session = {policySession:{authority:{requireAvailable(){}}},workflowId:'wf-actual',workflowNodeId:'review',kiroProxyRoute:{nodeId:'review',definitionId:'bundled://feature-pipeline',definitionRevision:ORIGINAL_SHA256+':0',nodePath:'code-loop/code-reviews/review-gpt',taskType:'code_review',executionId:'step-session',workflowId:'wf-actual'},workspace:{name:'mock'}}
 const services = servicesContext.servicesFixture.buildSessionServices(session)
 assert.equal(services.workflowId,session.workflowId)
 assert.equal(services.workflowNodeId,session.workflowNodeId)
 assert.equal(services.kiroProxyAutoDomain,true)
 assert.equal(servicesContext.servicesFixture.buildSessionServices(session,'group-1').kiroProxyAutoDomain,false)
 const optionsStart = candidate.indexOf('k={signal:A.signal,onRecoveryFired:_')+2
 const optionsEnd = candidate.indexOf(';l();let P=await g.stream(d,k)',optionsStart)
 assert.ok(optionsStart>1 && optionsEnd>optionsStart)
 Object.assign(servicesContext,{A:{signal:'mock-signal'},_:()=>{},C:()=>{},I:()=>{},h:'chat',f:{streamAttemptConsumer:{mock:true}},PAt:value=>value})
 servicesContext.execution = {sessionServices:services,model:{},chatSessionId:'step-session',subExecutionDepth:0,executionId:'main-execution'}
 const actualOptions = new Script('(function(){return '+candidate.slice(optionsStart,optionsEnd)+'}).call(execution)').runInContext(servicesContext)
 assert.equal(actualOptions.kiroProxyWorkflow.workflowId,'wf-actual')
 assert.equal(actualOptions.kiroProxyWorkflow.nodeId,'review')
 assert.equal(actualOptions.kiroProxyWorkflow.taskType,'code_review')
 assert.equal(actualOptions.kiroProxyWorkflow.executionId,'step-session')
 assert.equal(actualOptions.kiroProxyWorkflow.autoDomain,true)
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

test('workflow steps and ordinary subagents retain the root Auto control domain', () => {
 const installed = process.env.KIRO_STAGE_TEST_BUNDLE
 if (!installed) return
 const candidate = buildCandidate(readFileSync(installed))
 const context = runtime()
 context.Jve = value=>value && value!=='auto' ? value : undefined
 const stepStart = candidate.indexOf('function P8(t,e){')
 const stepEnd = candidate.indexOf('function HIt(t,e){',stepStart)
 assert.ok(stepStart>=0 && stepEnd>stepStart)
 new Script(candidate.slice(stepStart,stepEnd)).runInContext(context)
 assert.equal(context.P8({modelId:'group-1'},{modelId:'group-2',parentModelId:'auto'}),'auto')
 assert.equal(context.P8({modelId:'group-1'},{modelId:'auto',parentModelId:'group-2'}),'group-2')
 assert.equal(context.P8({modelId:'group-1'},{modelId:'group-2',parentModelId:'group-3'}),'group-3')
 assert.equal(context.P8({modelId:'auto'},{modelId:'auto',parentModelId:'group-3'}),'group-3')
 assert.equal(context.P8({modelId:'group-4'},{modelId:'group-2',parentModelId:'group-3'}),'group-3')
 assert.equal(context.P8({modelId:'group-1'},{modelId:'auto'}),'auto')
 assert.equal(runtime('0').kiroProxyEnvelopeV2(state,'wf-coder',workflow),undefined)
 context.process.env.KIRO_PROXY_STAGE_BRIDGE = '0'
 assert.equal(context.P8({modelId:'group-1'},{modelId:'group-2',parentModelId:'auto'}),'group-1')
 assert.equal(context.P8({modelId:'group-1'},{modelId:'group-2',parentModelId:'group-3'}),'group-1')
 context.process.env.KIRO_PROXY_STAGE_BRIDGE = '1'
 const modelStart = candidate.indexOf('let y=f.sessionServices.kiroProxyAutoDomain?')
 const modelEnd = candidate.indexOf(',w=cp()',modelStart)
 assert.ok(modelStart>=0 && modelEnd>modelStart)
 context.yoe = value=>({modelId:value})
 const chooseModel = new Script('(function(t,f,e){'+candidate.slice(modelStart,modelEnd)+';return y})').runInContext(context)
 assert.equal(chooseModel({inline:{resolvedModelId:'group-1'}},{sessionServices:{kiroProxyAutoDomain:true},model:{modelId:'parent-auto'}},{model:'agent-pinned'}),'auto')
 assert.equal(chooseModel({inline:{resolvedModelId:'group-1'}},{sessionServices:{kiroProxyAutoDomain:false},model:{modelId:'group-2'}},{model:'agent-pinned'}),'group-2')
 assert.equal(chooseModel({inline:{resolvedModelId:'group-1'}},{sessionServices:{kiroProxyAutoDomain:false},model:{modelId:'group-3'}},{model:'agent-pinned'}),'group-3')
 assert.equal(chooseModel({},{sessionServices:{kiroProxyAutoDomain:false},model:{modelId:'group-2'}},{model:'agent-pinned'}),'group-2')
 context.process.env.KIRO_PROXY_STAGE_BRIDGE = '0'
 assert.equal(chooseModel({inline:{resolvedModelId:'group-1'}},{sessionServices:{kiroProxyAutoDomain:false},model:{modelId:'group-2'}},{model:'agent-pinned'}),'group-1')
 context.process.env.KIRO_PROXY_STAGE_BRIDGE = '1'
 const copyStart = candidate.indexOf('sessionServices:process.env.KIRO_PROXY_STAGE_BRIDGE==="1"?{...f.sessionServices,kiroProxyRoute:')
 const copyEnd = candidate.indexOf(',promptContext:f.promptContext',copyStart)
 assert.ok(copyStart>=0 && copyEnd>copyStart)
 const makeServices = new Script('(function(f,t){return '+candidate.slice(copyStart+'sessionServices:'.length,copyEnd)+'})').runInContext(context)
 const parent = {sessionServices:{kiroProxyAutoDomain:true,kiroProxyRoute:{taskType:'orchestration',workflowId:''}},subExecutionDepth:0,executionId:'parent-execution'}
 const child = makeServices(parent,{})
 assert.notEqual(child,parent.sessionServices)
 assert.notEqual(child.kiroProxyRoute,parent.sessionServices.kiroProxyRoute)
 assert.equal(child.kiroProxyRoute.taskType,'orchestration')
 assert.equal(child.kiroProxyRoute.executionId,'parent-execution')
 const declared = makeServices(parent,{taskType:'investigation',executionId:'child-execution'})
 assert.equal(declared.kiroProxyRoute.taskType,'investigation')
 assert.equal(declared.kiroProxyRoute.executionId,'child-execution')
 assert.equal(parent.sessionServices.kiroProxyRoute.executionId,undefined)
})

test('two main executions in one session use their own execution IDs', () => {
 const installed = process.env.KIRO_STAGE_TEST_BUNDLE
 if (!installed) return
 const candidate = buildCandidate(readFileSync(installed))
 const context = runtime()
 Object.assign(context,{Vc:'mock-provider',d4d:()=>64000,d5r:()=>undefined,H5:()=>[],bis:()=>undefined,c:value=>value,A:{signal:'mock-signal'},_:()=>{},C:()=>{},I:()=>{},h:'chat',f:{streamAttemptConsumer:{}},PAt:value=>value})
 const servicesStart = candidate.indexOf('buildSessionServices(e,r){')
 const servicesEnd = candidate.indexOf('createDefinitionForMode(',servicesStart)
 const optionsStart = candidate.indexOf('k={signal:A.signal,onRecoveryFired:_')+2
 const optionsEnd = candidate.indexOf(';l();let P=await g.stream(d,k)',optionsStart)
 assert.ok(servicesStart>=0 && servicesEnd>servicesStart && optionsStart>1 && optionsEnd>optionsStart)
 new Script('class MainServicesFixture{requireSessionModelId(){return "auto"};'+candidate.slice(servicesStart,servicesEnd)+'};globalThis.mainServicesFixture=new MainServicesFixture').runInContext(context)
 const options = new Script('(function(){return '+candidate.slice(optionsStart,optionsEnd)+'})').runInContext(context)
 const session = {policySession:{authority:{requireAvailable(){}}},workspace:{name:'mock'}}
 const firstServices = context.mainServicesFixture.buildSessionServices(session)
 context.kiroProxyStampMainExecutionV2(session,{executionId:'main-first'})
 context.execution = {sessionServices:firstServices,model:{},chatSessionId:'same-session',subExecutionDepth:0,executionId:'main-first'}
 assert.equal(options.call(context.execution).kiroProxyWorkflow.executionId,'main-first')
 assert.equal(session.kiroProxyRoute.executionId,'main-first')
 const secondServices = context.mainServicesFixture.buildSessionServices(session)
 assert.equal(secondServices.kiroProxyRoute.executionId,undefined)
 context.kiroProxyStampMainExecutionV2(session,{executionId:'main-second'})
 context.execution = {sessionServices:secondServices,model:{},chatSessionId:'same-session',subExecutionDepth:0,executionId:'main-second'}
 assert.equal(options.call(context.execution).kiroProxyWorkflow.executionId,'main-second')
 assert.equal(session.kiroProxyRoute.executionId,'main-second')
 assert.equal(firstServices.kiroProxyRoute.executionId,undefined)
})

test('resident workflow step load restores the same private route on every continuation', async () => {
 const installed = process.env.KIRO_STAGE_TEST_BUNDLE
 if (!installed) return
 const candidate = buildCandidate(readFileSync(installed))
 const context = runtime()
 context.c = value=>value
 const definition = {steps:[{type:'step',id:'plan'}]}
 context.kiroProxyRecordRunV2('wf-resident',{definitionId:'bundled://feature-pipeline',bundleSha:ORIGINAL_SHA256})
 const session = {workflowId:'wf-resident',workflowNodeId:'plan',workspacePaths:['/mock'],workspace:{name:'mock'},policySession:{authority:{requireAvailable(){}}}}
 let coldLoads = 0
 const host = {getSession:()=>session,messageStore:{loadSession:async()=>null},loadServerSession:async()=>{coldLoads++}}
 const loadStart = candidate.indexOf('loadStepSession:c(async(t,e,r,n,s)=>{')
 const loadEnd = candidate.indexOf('getPersistedMessages:c(',loadStart)
 assert.ok(loadStart>=0 && loadEnd>loadStart)
 new Script('class ResidentFixture{constructor(host){this.host=host}bindStepToRun(){}restampLegacyStepSessionRoot(){}build(){return{'+candidate.slice(loadStart,loadEnd)+'}}};globalThis.residentFixture=new ResidentFixture(host)').runInContext(Object.assign(context,{host}))
 const load = context.residentFixture.build().loadStepSession
 const meta = {workflowId:'wf-resident',workflowName:'mock',nodeId:'plan',nodePath:['wf-resident','plan'],kiroProxyRevision:0,kiroProxyDefinition:definition}
 await load('stable-step-session','/mock',meta,'wf-planner',{})
 assert.equal(session.kiroProxyRoute.nodePath,'plan')
 assert.equal(session.kiroProxyRoute.taskType,'implementation_plan')
 assert.equal(session.kiroProxyRoute.executionId,'stable-step-session')
 assert.equal(session.kiroProxyRoute.definitionRevision,ORIGINAL_SHA256+':0')
 const firstRoute = session.kiroProxyRoute
 await load('stable-step-session','/mock',meta,'wf-planner',{})
 assert.notEqual(session.kiroProxyRoute,firstRoute)
 assert.equal(session.kiroProxyRoute.executionId,'stable-step-session')
 assert.equal(coldLoads,0)
 Object.assign(context,{Vc:'mock-provider',d4d:()=>64000,d5r:()=>undefined,H5:()=>[],bis:()=>undefined,A:{signal:'mock-signal'},_:()=>{},C:()=>{},I:()=>{},h:'chat',f:{streamAttemptConsumer:{}},PAt:value=>value})
 const servicesStart = candidate.indexOf('buildSessionServices(e,r){')
 const servicesEnd = candidate.indexOf('createDefinitionForMode(',servicesStart)
 const optionsStart = candidate.indexOf('k={signal:A.signal,onRecoveryFired:_')+2
 const optionsEnd = candidate.indexOf(';l();let P=await g.stream(d,k)',optionsStart)
 assert.ok(servicesStart>=0 && servicesEnd>servicesStart && optionsStart>1 && optionsEnd>optionsStart)
 new Script('class ResidentServicesFixture{requireSessionModelId(){return "auto"};'+candidate.slice(servicesStart,servicesEnd)+'};globalThis.residentServicesFixture=new ResidentServicesFixture').runInContext(context)
 const services = context.residentServicesFixture.buildSessionServices(session)
 const options = new Script('(function(){return '+candidate.slice(optionsStart,optionsEnd)+'})').runInContext(context)
 const request = {sessionServices:services,model:{},chatSessionId:'stable-step-session',subExecutionDepth:0,executionId:'ephemeral-drive-execution'}
 assert.equal(options.call(request).kiroProxyWorkflow.executionId,'stable-step-session')
 assert.equal(options.call(request).kiroProxyWorkflow.taskType,'implementation_plan')
 context.process.env.KIRO_PROXY_STAGE_BRIDGE = '0'
 delete session.kiroProxyRoute
 await load('stable-step-session','/mock',meta,'wf-planner',{})
 assert.equal(session.kiroProxyRoute,undefined)
 assert.equal(coldLoads,0)
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
 const headers = context.kiroProxyStageHeadersV2(state,'wf-coder',workflow)
 const response = await context.uFu({model:'auto',input:[]},{endpoint:'https://runtime.test',token:'mock',origin:'AI_EDITOR',kiroProxyWorkflowHeaders:headers})
 assert.equal(response.statusCode,200)
 assert.equal(captured.url,'https://runtime.test/v1/responses')
 assert.equal(captured.options.headers['X-Kiro-Proxy-Workflow'],headers['X-Kiro-Proxy-Workflow'])
 assert.equal(captured.options.headers.Authorization,'Bearer mock')
 assert.equal(JSON.parse(captured.body).model,'auto')
})
