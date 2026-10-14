import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import test from 'node:test'
import { createContext, Script } from 'node:vm'
import {spawnSync} from 'node:child_process'
import {fileURLToPath} from 'node:url'

const frontendRequire = createRequire(new URL('../frontend/package.json', import.meta.url))
const babelParser = frontendRequire('@babel/parser')
const nodeRequire = createRequire(import.meta.url)
const families = ['legacy', 'native']
const variants = ['original', 'alpha', 'quotes', 'outside', 'format', 'combined', 'collision']
const roleFixturePath = new URL('../proxy/kiro_stage_builtin_roles.json', import.meta.url)
const bridgeFunctionNames = [
 'kiroProxyDefinitionV2',
 'kiroProxyAgentRevisionV2',
 'kiroProxySourceFileV2',
 'kiroProxyWriteSourceV2',
 'kiroProxyReadSourceV2',
 'kiroProxyNodePathV2',
 'kiroProxyStepRouteV2',
 'kiroProxyEnvelopeV2',
 'kiroProxyStageHeadersV2',
 'kiroProxyAttachStageV2',
 'kiroProxyStageRawBodyV2',
 'kiroProxyVerifyBuiltinV3',
]

function visit(node, visitor, parent = undefined) {
 if (!node || typeof node !== 'object') return
 if (typeof node.type === 'string') visitor(node, parent)
 for (const [key, value] of Object.entries(node)) {
  if (key === 'loc' || key === 'start' || key === 'end' || key === 'extra' || key === 'leadingComments' || key === 'innerComments' || key === 'trailingComments') continue
  if (Array.isArray(value)) {
   for (const child of value) visit(child, visitor, node)
  } else if (value && typeof value === 'object') visit(value, visitor, node)
 }
}

function parseCandidate(source, filename) {
 return babelParser.parse(source, {
  sourceType: 'unambiguous',
  allowReturnOutsideFunction: true,
  allowAwaitOutsideFunction: true,
  sourceFilename: filename,
 })
}

function aliasNames(aliases) {
 const originalByActual = new Map()
 for (const [key, actual] of Object.entries(aliases)) {
  const split = key.lastIndexOf('@')
  assert.ok(split > 0 && /^\d+$/.test(key.slice(split + 1)), `alias key is not originalName@declarationOffset: ${key}`)
  assert.equal(typeof actual, 'string')
  assert.match(actual, /^kx\d+$/, `alpha alias is not offset-derived: ${actual}`)
  assert.equal(originalByActual.has(actual), false, `duplicate alpha binding alias: ${actual}`)
  originalByActual.set(actual, key.slice(0, split))
 }
 return originalByActual
}

function semanticName(actual, originalByActual) {
 return originalByActual.get(actual) ?? actual
}

function bindingNames(node) {
 if (!node) return []
 if (node.type === 'Identifier') return [node.name]
 if (node.type === 'RestElement') return bindingNames(node.argument)
 if (node.type === 'AssignmentPattern') return bindingNames(node.left)
 if (node.type === 'ArrayPattern') return node.elements.flatMap(bindingNames)
 if (node.type === 'ObjectPattern') return node.properties.flatMap(property => property.type === 'RestElement' ? bindingNames(property.argument) : bindingNames(property.value))
 return []
}

function topLevelNodes(ast) {
 return ast.program.body.map(node => node.type === 'ExportNamedDeclaration' || node.type === 'ExportDefaultDeclaration' ? node.declaration : node).filter(Boolean)
}

function extractBridgeBindings(source, ast, originalByActual) {
 const fragments = []
 const exposed = new Map()
 const add = (canonical, actual) => {
  assert.equal(exposed.has(canonical), false, `duplicate bridge declaration ${canonical}`)
  exposed.set(canonical, actual)
 }

 for (const node of topLevelNodes(ast)) {
  if (node.type === 'FunctionDeclaration' && node.id) {
   const canonical = semanticName(node.id.name, originalByActual)
   if (canonical.startsWith('kiroProxy')) {
    fragments.push({ start: node.start, source: source.slice(node.start, node.end) })
    add(canonical, node.id.name)
   }
   continue
  }
  if (node.type !== 'VariableDeclaration') continue
  const selected = []
  for (const declarator of node.declarations) {
   const names = bindingNames(declarator.id)
   const bridgeNames = names.filter(name => semanticName(name, originalByActual).startsWith('kiroProxy'))
   if (!bridgeNames.length) continue
   selected.push(source.slice(declarator.start, declarator.end))
   for (const actual of bridgeNames) add(semanticName(actual, originalByActual), actual)
  }
  if (selected.length) fragments.push({ start: node.start, source: `${node.kind} ${selected.join(',')};` })
 }

 assert.ok(exposed.has('kiroProxyBuiltinRolesV3'), 'static bridge extraction omitted kiroProxyBuiltinRolesV3')
 for (const name of bridgeFunctionNames) assert.ok(exposed.has(name), `static bridge extraction omitted ${name}`)
 fragments.sort((left, right) => left.start - right.start)
 const exports = [...exposed].map(([canonical, actual]) => `${JSON.stringify(canonical)}:${actual}`).join(',')
 const code = `${fragments.map(fragment => fragment.source).join('\n')}\nglobalThis.__kiroBridge={${exports}};`
 return { code, exposed }
}

function moduleSpecifiers(node) {
 const found = new Set()
 visit(node, current => {
  if (current.type === 'ImportDeclaration' && current.source?.type === 'StringLiteral') found.add(current.source.value)
  if (current.type === 'CallExpression' && current.callee?.type==='Identifier' && current.callee.name==='require' && current.arguments?.[0]?.type === 'StringLiteral') {
   const value = current.arguments[0].value
   if (value === 'path' || value === 'crypto' || value.startsWith('node:')) found.add(value)
  }
 })
 return found
}

function importedBindings(ast, originalByActual) {
 const bindings = new Map()
 for (const node of topLevelNodes(ast)) {
  if (node.type === 'ImportDeclaration') {
   for (const specifier of node.specifiers) bindings.set(specifier.local.name, { specifier: node.source.value, semantic: semanticName(specifier.local.name, originalByActual) })
   continue
  }
  if (node.type !== 'VariableDeclaration') continue
  for (const declarator of node.declarations) {
   const specs = moduleSpecifiers(declarator.init)
   if (specs.size !== 1) continue
   for (const actual of bindingNames(declarator.id)) bindings.set(actual, { specifier: [...specs][0], semantic: semanticName(actual, originalByActual) })
  }
 }
 return new Map([...bindings].map(([actual, specifier]) => [actual, specifier]))
}

function helperFunctionNode(ast, extracted, originalByActual, canonicalName) {
 const actual = extracted.exposed.get(canonicalName)
 const matches = []
 for (const node of topLevelNodes(ast)) {
  if (node.type === 'FunctionDeclaration' && node.id?.name === actual) matches.push(node)
  if (node.type === 'VariableDeclaration') {
   for (const declaration of node.declarations) {
    if (bindingNames(declaration.id).includes(actual)) matches.push(declaration.init)
   }
  }
 }
 const fn = matches.find(node => node && (node.type === 'FunctionDeclaration' || node.type === 'FunctionExpression' || node.type === 'ArrowFunctionExpression'))
 assert.ok(fn, `could not locate AST function ${canonicalName}`)
 return fn
}

function recipeGetterIdentifier(definitionNode) {
 const names = new Set()
 visit(definitionNode.body, node => {
  if (node.type === 'ForOfStatement' && node.right?.type === 'CallExpression' && node.right.callee?.type === 'Identifier') names.add(node.right.callee.name)
 })
 assert.equal(names.size, 1, 'kiroProxyDefinitionV2 must expose exactly one recipe getter call in its AST')
 return [...names][0]
}

function directCallIdentifiers(node) {
 const names = new Set()
 visit(node.body ?? node, current => {
  if (current.type === 'CallExpression' && current.callee?.type === 'Identifier') names.add(current.callee.name)
 })
 return names
}

function createBridgeRuntime(source, filename, aliases, recipes) {
 const ast = parseCandidate(source, filename)
 const originalByActual = aliasNames(aliases)
 const extracted = extractBridgeBindings(source, ast, originalByActual)
 const imported = importedBindings(ast, originalByActual)
 const allowedModules = new Set([
  ...moduleSpecifiers(parseCandidate(extracted.code, `${filename}#bridge-helpers`).program),
  ...[...imported.values()].map(binding => binding.specifier),
 ])
 const requireCandidateModule = specifier => {
  assert.ok(allowedModules.has(specifier), `candidate AST does not reference requested module ${specifier}`)
  return nodeRequire(specifier)
 }
 const context = createContext({
  Buffer,
  process: { env: { KIRO_PROXY_STAGE_BRIDGE: '1' } },
  require: requireCandidateModule,
 })
 for (const [actual, binding] of imported) {
  if (allowedModules.has(binding.specifier)) context[actual] = requireCandidateModule(binding.specifier)
 }

 const definitionNode = helperFunctionNode(ast, extracted, originalByActual, 'kiroProxyDefinitionV2')
 const recipeGetter = recipeGetterIdentifier(definitionNode)
 context[recipeGetter] = () => recipes.map(workflow => ({ name: workflow.name, workflow }))

 const sourcePathNode = helperFunctionNode(ast, extracted, originalByActual, 'kiroProxySourceFileV2')
 for (const actual of directCallIdentifiers(sourcePathNode)) {
  const canonical = semanticName(actual, originalByActual)
  if (canonical.startsWith('kiroProxy') || actual === 'require' || Object.hasOwn(context, actual)) continue
  context[actual] = (...parts) => path.join(...parts)
 }

 new Script(extracted.code, { filename: `${filename}#bridge-helpers` }).runInContext(context)
 return {
  ast,
  context,
  bridge: context.__kiroBridge,
  originalByActual,
  extracted,
  filename,
  source,
  aliases,
 }
}

function roleRecipes(roles) {
 const recipes = []
 for (const [identity, expected] of Object.entries(roles)) {
  const root = { children: new Map() }
  for (const [nodePath, agent] of Object.entries(expected)) {
   const parts = nodePath.split('/')
   assert.ok(parts.length > 0 && parts.every(Boolean), `invalid builtin node path ${identity}/${nodePath}`)
   let current = root
   for (const part of parts) {
    if (!current.children.has(part)) current.children.set(part, { children: new Map() })
    current = current.children.get(part)
   }
   assert.equal(current.agent, undefined, `duplicate builtin node path ${identity}/${nodePath}`)
   current.agent = agent
  }
  const materialize = (id, node) => {
   if (node.agent !== undefined) {
    assert.equal(node.children.size, 0, `builtin step has children ${identity}/${id}`)
    return { type: 'step', id, agent: node.agent }
   }
   return { type: 'sequence', id, steps: [...node.children].map(([childId, child]) => materialize(childId, child)) }
  }
  const name = identity.replace(/^bundled:\/\//, '')
  recipes.push({ name, steps: [...root.children].map(([id, node]) => materialize(id, node)) })
 }
 return recipes
}

function stepAt(workflow, nodePath) {
 let nodes = workflow.steps
 let current
 for (const id of nodePath.split('/')) {
  current = nodes.find(node => node.id === id)
  if (!current) return undefined
  nodes = current.type === 'parallel' ? current.branches : current.steps
 }
 return current
}

function assertRoleFixtureMatches(roles, recipes) {
 for (const [identity, expected] of Object.entries(roles)) {
  const workflow = recipes.find(recipe => `bundled://${recipe.name}` === identity)
  assert.ok(workflow, `missing recipe fixture ${identity}`)
  for (const [nodePath, agent] of Object.entries(expected)) {
   const step = stepAt(workflow, nodePath)
   assert.equal(step?.type, 'step', `${identity}/${nodePath} is not an LLM step`)
   assert.equal(step.agent, agent, `agent mismatch at ${identity}/${nodePath}`)
  }
 }
}

function cloneRecipes(recipes) {
 return JSON.parse(JSON.stringify(recipes))
}

function stateForAuto() {
 return {
  conversationId: 'wire-conversation',
  agentContinuationId: 'continuation-1',
  currentMessage: {
   userInputMessage: {
    modelId: 'auto',
    userInputMessageContext: { toolResults: [{ toolUseId: 'tool-1' }] },
   },
  },
 }
}

function workflowMeta(overrides = {}) {
 return {
  workflowId: 'workflow-1',
  nodeId: 'review',
  definitionId: 'bundled://feature-pipeline',
  definitionRevision: 'bundle-sha:0',
  nodePath: 'code-loop/code-reviews/review-gpt',
  taskType: 'code_review',
  executionId: 'step-session',
  autoDomain: true,
  ...overrides,
 }
}

async function replayMetadataAndTransport(runtime) {
 const { bridge, context } = runtime
 const state = stateForAuto()
 const workflow = workflowMeta()
 const metadata = bridge.kiroProxyEnvelopeV2(state, 'wf-reviewer', workflow)
 assert.equal(metadata.version, 2)
 assert.equal(metadata.workflowId, workflow.workflowId)
 assert.equal(metadata.taskType, workflow.taskType)
 assert.equal(metadata.conversationId, state.conversationId)
 assert.equal(metadata.agentContinuationId, state.agentContinuationId)

 assert.equal(bridge.kiroProxyEnvelopeV2(state, 'wf-reviewer', {}), undefined)
 assert.equal(bridge.kiroProxyEnvelopeV2(state, 'wf-reviewer', workflowMeta({ autoDomain: false })), undefined)
 assert.equal(bridge.kiroProxyEnvelopeV2(state, 'wf-reviewer', workflowMeta({ autoDomain: 'true' })), undefined)
 assert.throws(() => bridge.kiroProxyEnvelopeV2({ ...state, currentMessage: { userInputMessage: { modelId: 'manual-model' } } }, 'wf-reviewer', workflow), /Auto request model/)
 assert.throws(() => bridge.kiroProxyEnvelopeV2({ ...state, conversationId: '' }, 'wf-reviewer', workflow), /conversationId/)
 assert.throws(() => bridge.kiroProxyEnvelopeV2(state, 'wf-reviewer', workflowMeta({ nodeId: 7 })), /nodeId/)

 const headers = bridge.kiroProxyStageHeadersV2(state, 'wf-reviewer', workflow)
 assert.deepEqual(Object.keys(headers), ['X-Kiro-Proxy-Workflow'])
 assert.deepEqual(JSON.parse(headers['X-Kiro-Proxy-Workflow']), JSON.parse(JSON.stringify(metadata)))

 let registration
 const command = { middlewareStack: { add(middleware, options) { registration = { middleware, options } } } }
 assert.equal(bridge.kiroProxyAttachStageV2(command, state, 'wf-reviewer', workflow), command)
 assert.equal(registration.options.step, 'finalizeRequest')
 const next = registration.middleware(async args => args)
 const sdkResult = await next({ request: { headers: { Authorization: 'Bearer mock', Existing: 'yes' } } })
 assert.equal(sdkResult.request.headers.Authorization, 'Bearer mock')
 assert.equal(sdkResult.request.headers.Existing, 'yes')
 assert.deepEqual(JSON.parse(sdkResult.request.headers['X-Kiro-Proxy-Workflow']), JSON.parse(JSON.stringify(metadata)))

 const originalBody = {
  model: 'auto',
  input: [{ type: 'function_call_output', call_id: 'tool-1', output: 'done' }],
  metadata: { 'kiro:conversation_id': state.conversationId, retained: 'yes' },
 }
 const body = bridge.kiroProxyStageRawBodyV2(originalBody, state, 'wf-reviewer', workflow)
 assert.notEqual(body, originalBody)
 assert.deepEqual(body.input, originalBody.input)
 assert.equal(body.metadata.retained, 'yes')
 assert.equal(body.metadata['kiro:conversation_id'], state.conversationId)
 assert.equal(body.metadata['kiro:agent_mode'], 'wf-reviewer')
 assert.equal(body.metadata['kiro:proxy_continuation_id'], state.agentContinuationId)
 assert.equal(body.metadata['kiro:proxy_current_tool_results'], 1)
 assert.equal(originalBody.metadata['kiro:proxy_current_tool_results'], undefined)
 assert.throws(() => bridge.kiroProxyStageRawBodyV2({ ...originalBody, model: 'manual-model' }, state, 'wf-reviewer', workflow), /Auto raw model/)
 assert.equal(bridge.kiroProxyStageRawBodyV2(originalBody, state, 'wf-reviewer', workflowMeta({ autoDomain: false })), originalBody)
 assert.equal(context.process.env.KIRO_PROXY_STAGE_BRIDGE, '1')
}

// 除 helpers 外，执行候选里的真实 SDK 方法和 raw body 函数，验证跨补丁变量捕获。
async function replayActualTransportHooks(runtime) {
 const {ast,source,context,originalByActual}=runtime
 const methods=[]
 const rawFunctions=[]
 visit(ast.program,node=>{
  if(node.type==='ClassMethod'&&node.key?.name==='buildRequestCommand')methods.push(node)
  if(node.type==='FunctionDeclaration'&&!node.id?.name.startsWith('kiroProxy')){
   let usesBridge=false
   visit(node.body,child=>{if(child.type==='CallExpression'&&child.callee?.name==='kiroProxyStageRawBodyV2')usesBridge=true})
   if(usesBridge)rawFunctions.push(node)
  }
 })
 assert.equal(methods.length,1)
 assert.equal(rawFunctions.length,1)
 const method=methods[0]
 let constructorName
 visit(method.body,node=>{if(node.type==='NewExpression'&&node.callee.type==='Identifier')constructorName=node.callee.name})
 assert.ok(constructorName)
 let registration
 context[constructorName]=class{constructor(input){this.input=input;this.middlewareStack={add(middleware,options){registration={middleware,options}}}}}
 new Script(`class ActualSDKFixture{agentMode='wf-coder';emitTestModelCallerHeaders=false;${source.slice(method.start,method.end)}};globalThis.actualSDKFixture=new ActualSDKFixture()`).runInContext(context)
 const state=stateForAuto(),workflow=workflowMeta()
 const command=context.actualSDKFixture.buildRequestCommand({},state,undefined,undefined,false,workflow)
 assert.equal(command.input.conversationState,state)
 assert.equal(registration.options.step,'finalizeRequest')
 const result=await registration.middleware(async args=>args)({request:{headers:{Authorization:'Bearer mock'}}})
 assert.equal(result.request.headers.Authorization,'Bearer mock')
 assert.equal(JSON.parse(result.request.headers['X-Kiro-Proxy-Workflow']).workflowId,workflow.workflowId)
 const raw=rawFunctions[0]
 for(const actual of directCallIdentifiers(raw)){
  const canonical=semanticName(actual,originalByActual)
  if(canonical==='kiroProxyStageRawBodyV2')continue
  if(canonical==='iJu'||canonical==='iFu')context[actual]=()=>[{type:'function',name:'mock-tool'}]
  else if(canonical==='oJu'||canonical==='oFu')context[actual]=()=>[{type:'function_call_output',call_id:'tool-1',output:'done'}]
  else if(canonical==='aJu'||canonical==='aFu')context[actual]=()=>({})
  else assert.fail(`unexpected raw body dependency ${canonical}`)
 }
 new Script(`${source.slice(raw.start,raw.end)};globalThis.actualRawBody=${raw.id.name}`).runInContext(context)
 const body=context.actualRawBody({conversationState:state,modelId:'auto',origin:'AI_EDITOR',agentMode:'wf-coder',kiroProxyWorkflow:workflow})
 assert.equal(body.model,'auto')
 assert.equal(body.input[0].call_id,'tool-1')
 assert.equal(body.metadata['kiro:conversation_id'],state.conversationId)
 assert.equal(body.metadata['kiro:proxy_continuation_id'],state.agentContinuationId)
 assert.equal(body.metadata['kiro:proxy_current_tool_results'],1)
}

async function replayActualRegisteredChild(runtime){
 const {ast,source,context,originalByActual}=runtime
 const functions=topLevelNodes(ast).filter(node=>node.type==='FunctionDeclaration'&&semanticName(node.id?.name,originalByActual)==='I3r')
 assert.equal(functions.length,1)
 const fn=functions[0]
 for(const scenario of [{taskType:'investigation'}, {}, {manual:true}, {disabled:true,manual:true}]){
  let built,child,nativeCalls=0
  const globals={
   I3e:()=>({modelId:'pinned-model'}),tm:()=> 'mock-conversation',_3r:x=>x,Tye:x=>x,sSt:x=>x,
   a8:()=> 'fallback prompt',YPt:async x=>x,b3r:()=>undefined,EQ:x=>x,
   Nud:()=>({needsFileTree:false,async buildDefinition(x){built=x;return{}}}),Wud:()=>false,
   fx:{},ob:()=>({}),Q$e:()=>({}),G$e:()=>8192,iSt:()=>({}),hA:{SpecGeneration:'spec-generation'},
   A3e:{id:'mock-parent-agent'},C:{info(){},warn(){},debug(){},error(){}},
   WHe:(_definition,options)=>{child=options;throw new Error('captured child options')},Bn:()=>false,
  }
  visit(fn,node=>{if(node.type==='Identifier'){const canonical=semanticName(node.name,originalByActual);if(Object.hasOwn(globals,canonical))context[node.name]=globals[canonical]}})
  new Script(`${source.slice(fn.start,fn.end)};globalThis.actualRegisteredChild=${fn.id.name}`).runInContext(context)
  context.process.env.KIRO_PROXY_STAGE_BRIDGE=scenario.disabled?'0':'1'
  const route={nodeId:'review',definitionId:'bundled://feature-pipeline',definitionRevision:'rev-1',nodePath:'plan/review',taskType:'implementation_plan',executionId:'parent-run',workflowId:'wf-parent'}
  const services={kiroProxyAutoDomain:!scenario.manual,kiroProxyRoute:route,modelRouting:{subAgentStartCategory(){nativeCalls++;return 'native-category'}}}
  const promptContext={specWorkflow:{workflowId:'wf-parent',nodeId:'review'},specSkipClarificationEnabled:false}
  const parent={getEventSink:()=>({}),workspace:{withToolPolicy:()=>({withContext:()=>({})})},sessionServices:services,getAgentContext:()=>({}),getAgentMode:()=> 'workflow',promptContext,
   model:{modelId:'parent-model',thinkingType:'balanced',choices:{effortLevel:'balanced'}},abortController:{signal:{}},subExecutionDepth:1,chatSessionId:'parent-chat',autonomyMode:'auto',steering:[],repositories:[],knowledgeListing:[]}
  const messages=[{kind:'mock-context'}]
  const result=await context.actualRegisteredChild({definition:{id:'wf-reviewer',model:'pinned-model',prompt:'review'},caller:'InvokeSubAgent',agentName:'wf-reviewer',prompt:'review',registry:{},contextProviderRegistry:{},parentExecution:parent,contextMessages:messages,executionId:'child-run',taskType:scenario.taskType,categoryModelId:'category-strong',events:{forwardToParent:true}})
  assert.equal(result.kind,'fault')
  assert.ok(child,'真实函数必须走到子执行构造')
  assert.equal(built.contextMessages,messages)
  assert.equal(built.executionId,'child-run')
  assert.equal(built.specWorkflow,promptContext.specWorkflow)
  assert.equal(child.model.modelId,scenario.manual?'native-category':'auto')
  assert.equal(nativeCalls,scenario.manual?1:0)
  assert.equal(child.sessionServices.kiroProxyRoute.taskType,scenario.taskType??route.taskType)
  assert.equal(child.sessionServices.kiroProxyRoute.executionId,scenario.taskType?'child-run':route.executionId)
  assert.equal(services.kiroProxyRoute,route)
  if(scenario.disabled)assert.equal(child.sessionServices,services)
  else assert.notEqual(child.sessionServices,services)
 }
 context.process.env.KIRO_PROXY_STAGE_BRIDGE='1'
}

function mockFs() {
 const files = new Map()
 return {
  files,
  async writeFile(file, value) { files.set(file, value) },
  async rename(from, to) {
   if (!files.has(from)) throw new Error(`mock rename source missing: ${from}`)
   files.set(to, files.get(from))
   files.delete(from)
  },
  async readFile(file) {
   if (!files.has(file)) {
    const error = new Error('mock file not found')
    error.code = 'ENOENT'
    throw error
   }
   return files.get(file)
  },
  async unlink(file) { files.delete(file) },
 }
}

async function replaySourceFilesAndChildIdentity(runtime, recipes) {
 const { bridge } = runtime
 const files = mockFs()
 const revision = bridge.kiroProxyAgentRevisionV2(
  { name: 'agent-wf-reviewer', steps: [{ type: 'step', id: 'run', agent: 'wf-reviewer' }] },
  'investigation',
 )
 assert.match(revision, /^[a-f0-9]{64}$/)
 assert.notEqual(revision, bridge.kiroProxyAgentRevisionV2(
  { name: 'agent-wf-reviewer', steps: [{ type: 'step', id: 'run', agent: 'wf-reviewer' }] },
  'design',
 ))

 const base = path.join('mock-runs', 'workflows')
 const inheritedWorkflowId = 'child-inherited'
 const parentRoute = {
  nodeId: 'review-gpt',
  definitionId: 'bundled://feature-pipeline',
  definitionRevision: 'parent-revision',
  nodePath: 'code-loop/code-reviews/review-gpt',
  taskType: 'code_review',
  executionId: 'parent-execution',
  workflowId: 'parent-workflow',
 }
 await bridge.kiroProxyWriteSourceV2(files, base, inheritedWorkflowId, {
  definitionId: '', definitionRevision: '', taskType: '', bundleSha: '', parentRoute,
 })
 const sourceFile = bridge.kiroProxySourceFileV2(base, inheritedWorkflowId)
 assert.ok(sourceFile.endsWith(path.join('child-inherited', 'kiro-proxy-routing-v2.json')))
 assert.ok(files.files.has(sourceFile), 'source helper did not write to the Node path-derived destination')
 const fresh = createBridgeRuntime(runtime.source, runtime.filename, runtime.aliases, recipes)
 const restored = await fresh.bridge.kiroProxyReadSourceV2(files, base, inheritedWorkflowId)
 assert.equal(restored.parentRoute.taskType, parentRoute.taskType)
 const childWorkflow = { name: 'agent-wf-reviewer', steps: [{ type: 'step', id: 'run', agent: 'wf-reviewer' }] }
 const inherited = fresh.bridge.kiroProxyStepRouteV2({
  workflowId: inheritedWorkflowId,
  nodeId: 'run',
  nodePath: [inheritedWorkflowId, 'run'],
  kiroProxyDefinition: childWorkflow,
 }, 'child-session', 0)
 assert.equal(inherited.taskType, parentRoute.taskType)
 assert.equal(inherited.executionId, parentRoute.executionId)
 assert.equal(inherited.workflowId, inheritedWorkflowId)

 const explicitWorkflowId = 'child-explicit'
 await bridge.kiroProxyWriteSourceV2(files, base, explicitWorkflowId, {
  definitionId: 'agent://wf-reviewer',
  definitionRevision: revision,
  taskType: 'investigation',
  bundleSha: '',
  parentRoute,
 })
 const explicit = await fresh.bridge.kiroProxyReadSourceV2(files, base, explicitWorkflowId)
 assert.equal(explicit.taskType, 'investigation')
 const explicitRoute = fresh.bridge.kiroProxyStepRouteV2({
  workflowId: explicitWorkflowId,
  nodeId: 'run',
  nodePath: [explicitWorkflowId, 'run'],
  kiroProxyDefinition: childWorkflow,
 }, 'explicit-child-session', 0)
 assert.equal(explicitRoute.nodePath, 'run')
 assert.equal(explicitRoute.definitionId, 'agent://wf-reviewer')
 assert.equal(explicitRoute.taskType, 'investigation')
 assert.equal(explicitRoute.executionId, 'explicit-child-session')
}

function freeCallIdentifiers(method) {
 const local = new Set(method.params.flatMap(bindingNames))
 visit(method.body, node => {
  if (node.type === 'VariableDeclarator') for (const name of bindingNames(node.id)) local.add(name)
  if (node.type === 'FunctionDeclaration' && node.id) local.add(node.id.name)
  if (node.type === 'CatchClause') for (const name of bindingNames(node.param)) local.add(name)
 })
 const calls = new Set()
 visit(method.body, (node, parent) => {
  if (node.type !== 'CallExpression' || node.callee?.type !== 'Identifier' || local.has(node.callee.name)) return
  calls.add(node.callee.name)
 })
 return calls
}

function thisMethodNames(method) {
 const names = new Set()
 visit(method.body, node => {
  if (node.type !== 'CallExpression' || node.callee?.type !== 'MemberExpression' || node.callee.object?.type !== 'ThisExpression') return
  if (!node.callee.computed && node.callee.property?.type === 'Identifier') names.add(node.callee.property.name)
  if (node.callee.computed && node.callee.property?.type === 'StringLiteral') names.add(node.callee.property.value)
 })
 return [...names]
}

function nativeSessionFixture(source, filename, aliases, enabled) {
 const ast = parseCandidate(source, filename)
 const originalByActual = aliasNames(aliases)
 const methods = []
 visit(ast.program, node => {
  if (node.type !== 'ClassMethod' && node.type !== 'ObjectMethod') return
  const key = node.key?.type === 'Identifier' ? node.key.name : node.key?.value
  if (key === 'buildSessionServices') methods.push(node)
 })
 assert.equal(methods.length, 1, `${filename} must have one actual buildSessionServices method`)
 const method = methods[0]
 const parameters = method.params.map(pattern => bindingNames(pattern).map(actual => ({ actual, semantic: semanticName(actual, originalByActual) })))
 assert.ok(parameters.length > 0, `${filename} buildSessionServices has no AST parameters`)
 const methodSource = source.slice(method.start, method.end)
 const context = createContext({
  Buffer,
  process: { env: { KIRO_PROXY_STAGE_BRIDGE: enabled ? '1' : '0' } },
  require: specifier => { throw new Error(`unexpected module access in session fixture: ${specifier}`) },
 })
 // 从当前候选提取实际环境别名声明，使独立方法拥有与整包相同的词法依赖。
 const environmentDeclarations = topLevelNodes(ast).flatMap(node => node.type !== 'VariableDeclaration' ? [] :
  node.declarations.filter(declaration => {
   const names = bindingNames(declaration.id)
   return names.length > 0 && names.every(name => semanticName(name, originalByActual).startsWith('kiroProxyGlobalV3_'))
  }).map(declaration => `${node.kind} ${source.slice(declaration.start, declaration.end)};`))
 assert.ok(environmentDeclarations.length > 0, 'session fixture must use the actual candidate environment aliases')
 new Script(environmentDeclarations.join('\n'), { filename: `${filename}#environment-aliases` }).runInContext(context)
 for (const [actual, binding] of importedBindings(ast, originalByActual)) {
  if (binding.specifier === 'path' || binding.specifier === 'crypto' || binding.specifier.startsWith('node:')) {
   context[actual] = nodeRequire(binding.specifier)
  }
 }
 for (const actual of freeCallIdentifiers(method)) {
  if (!Object.hasOwn(context, actual) && !(actual in globalThis)) {
   context[actual] = (...args) => typeof args[0] === 'function' ? args[0] : { modelId: args[0] }
  }
 }
 const calls = JSON.stringify(thisMethodNames(method))
 const fixtureScript = `class SessionServicesFixture{
  constructor(){this.nativeRoutingCalls=0;this.fallbackCalls=0;for(const name of ${calls})if(typeof this[name]!=="function")this[name]=()=>({mockDependency:name})}
  requireSessionModelId(){return "auto"}
  modelRoutingConfigInput(){return {settingEnabled:true}}
  modelRoutingForExecution(){this.nativeRoutingCalls++;return {currentCategory:()=>"native-category",thinkingType:()=>"balanced"}}
  buildModelFallbackService(){this.fallbackCalls++;return {kind:"native-fallback"}}
  acquireSubagentMcp(){return {kind:"mock-mcp"}}
  ${methodSource}
 };globalThis.__sessionFixture=new SessionServicesFixture()`
 new Script(fixtureScript, { filename: `${filename}#buildSessionServices` }).runInContext(context)
 const fixture = context.__sessionFixture
 return { fixture, context, originalByActual, method, parameters }
}

function invokeBuildSessionServices(fixture, method, session, request, requestedModel) {
 const argumentsFromContract = [session, request, requestedModel]
 return fixture.buildSessionServices(...argumentsFromContract.slice(0, method.params.length))
}

function replayNativeRoutingAndFallback(source, filename, aliases) {
 for (const enabled of [true, false]) {
  const { fixture, context, method, parameters } = nativeSessionFixture(source, filename, aliases, enabled)
  const signature = parameters.map(group => group.map(binding => binding.semantic).join('+')).join(',')
  const session = {
   sessionId: 'session-1',
   workspace: { name: 'mock-workspace' },
   policySession: { authority: { requireAvailable() {} } },
  }
  const automatic = invokeBuildSessionServices(fixture, method, session, { cursor: 0 }, 'auto')
  if (enabled) {
   assert.equal(automatic.model.modelId, 'auto', `${filename}(${signature}): native routing replaced bridge Auto`)
   assert.equal(automatic.kiroProxyAutoDomain, true)
   assert.equal(automatic.modelRouting, undefined)
   assert.equal(automatic.modelFallback, undefined)
   assert.equal(fixture.nativeRoutingCalls, 0)
   assert.equal(fixture.fallbackCalls, 0)
   const overridden = invokeBuildSessionServices(fixture, method, { ...session, sessionId: 'session-2' }, { cursor: 0, override: { modelId: 'fallback-model' } }, 'auto')
   assert.equal(overridden.model.modelId, 'auto', `${filename}(${signature}): override/fallback displaced bridge Auto`)
   assert.equal(overridden.modelFallback, undefined)
   assert.equal(fixture.nativeRoutingCalls, 0)
   assert.equal(fixture.fallbackCalls, 0)
  } else {
   assert.equal(automatic.model.modelId, 'native-category')
   assert.equal(automatic.kiroProxyAutoDomain, false)
   assert.ok(automatic.modelFallback)
   assert.equal(fixture.nativeRoutingCalls, 1)
   assert.equal(fixture.fallbackCalls, 1)
  }

  const outsideAuto = invokeBuildSessionServices(fixture, method, { ...session, sessionId: `manual-${enabled}` }, { cursor: 0 }, 'manual-model')
  assert.equal(outsideAuto.kiroProxyAutoDomain, false)
  assert.equal(outsideAuto.model.modelId, 'native-category')
  assert.ok(outsideAuto.modelFallback)
  assert.equal(context.process.env.KIRO_PROXY_STAGE_BRIDGE, enabled ? '1' : '0')
 }
}

function loadAliases(outputDirectory, family, variant) {
 if (variant !== 'alpha' && variant !== 'combined') return {}
 const aliasPath = path.join(outputDirectory, `${family}-aliases.json`)
 const aliases = JSON.parse(readFileSync(aliasPath, 'utf8'))
 assert.ok(Object.keys(aliases).length > 0, `${aliasPath} must describe alpha-renamed bindings`)
 return aliases
}

test('compiler candidates preserve Auto metadata, stage transport, source identity, and builtin duties', async t => {
 const outputDirectory = process.env.KIRO_STAGE_AUTO_TEST_OUTPUT
 if (!outputDirectory) {
  t.skip('KIRO_STAGE_AUTO_TEST_OUTPUT is not set; waiting for compiler candidate artifacts')
  return
 }
 assert.ok(existsSync(outputDirectory), `candidate output directory does not exist: ${outputDirectory}`)
 const selected=process.env.KIRO_STAGE_AUTO_TEST_VARIANT
 if(!selected){
  // 每个完整 AST 约占 1 GB；单独进程回放，避免测试框架保留子测试闭包累计整包 AST。
  for(const family of families)for(const variant of variants)await t.test(`${family}-${variant}`,()=>{
   const childEnv={...process.env,KIRO_STAGE_AUTO_TEST_VARIANT:`${family}-${variant}`}
   delete childEnv.NODE_TEST_CONTEXT
   const result=spawnSync(process.execPath,['--test',fileURLToPath(import.meta.url)],{
    env:childEnv,
    encoding:'utf8',windowsHide:true,timeout:120000,maxBuffer:4*1024*1024,
   })
   assert.equal(result.error,undefined,result.error?.message)
   assert.equal(result.status,0,result.stdout+'\n'+result.stderr)
   assert.match(result.stdout,/AST-extracted helpers replay Auto protocol and source I\/O/,'子进程必须实际执行行为回放')
  })
  return
 }
 const roles = JSON.parse(readFileSync(roleFixturePath, 'utf8'))
 const recipes = roleRecipes(roles)
 const dutyCount = Object.values(roles).reduce((sum, entries) => sum + Object.keys(entries).length, 0)
 assert.equal(dutyCount, 20, 'builtin role fixture must cover the 20 LLM duty nodes')
 assertRoleFixtureMatches(roles, recipes)

 const candidates = new Map()
 for (const family of families) {
  for (const variant of variants) {
   if(`${family}-${variant}`!==selected)continue
   const filename = `${family}-${variant}.js`
   const candidatePath = path.join(outputDirectory, filename)
   const source = readFileSync(candidatePath, 'utf8')
   const aliases = loadAliases(outputDirectory, family, variant)
   const runtime = createBridgeRuntime(source, candidatePath, aliases, recipes)
   if(family==='native'&&(variant==='original'||variant==='alpha'))candidates.set(`${family}-${variant}`, {source, aliases, filename:candidatePath})
   await t.test(`${filename}: AST-extracted helpers replay Auto protocol and source I/O`, async () => {
    await replayMetadataAndTransport(runtime)
    await replayActualTransportHooks(runtime)
    if(family==='native'&&(variant==='original'||variant==='alpha'))await replayActualRegisteredChild(runtime)
    await replaySourceFilesAndChildIdentity(runtime, recipes)
    const seen = new Set()
    let checked = 0
    for (const [identity, expected] of Object.entries(roles)) {
     const recipe = recipes.find(item => `bundled://${item.name}` === identity)
     const definition = runtime.bridge.kiroProxyDefinitionV2(recipe)
     assert.equal(definition.definitionId, identity)
     runtime.bridge.kiroProxyRecordRunV2(recipe.name, definition)
     runtime.bridge.kiroProxyVerifyBuiltinV3(recipe, identity)
     for (const [nodePath, agent] of Object.entries(expected)) {
      const step = stepAt(recipe, nodePath)
      assert.equal(step.agent, agent, `${identity}/${nodePath}`)
      const route = runtime.bridge.kiroProxyStepRouteV2({
       workflowId: recipe.name,
       nodeId: nodePath.split('/').at(-1),
       nodePath: [recipe.name, ...nodePath.split('/')],
       kiroProxyDefinition: recipe,
      }, `step-${checked}`, 0)
      assert.equal(route.definitionId, identity)
      assert.ok(route.taskType, `${identity}/${nodePath} lost its declared LLM duty`)
      seen.add(`${identity}/${nodePath}`)
      checked++
     }
    }
    assert.equal(checked, 20)
    assert.equal(seen.size, 20)

    const changed = cloneRecipes(recipes)
    const changedWorkflow = changed.find(item => item.name === 'feature-pipeline')
    const changedStep = stepAt(changedWorkflow, 'setup')
    changedStep.agent = `${changedStep.agent}-changed`
    assert.throws(
     () => runtime.bridge.kiroProxyVerifyBuiltinV3(changedWorkflow, 'bundled://feature-pipeline'),
     /Kiro built-in recipe agent changed/,
    )
   })
  }
 }

 for (const variant of ['original', 'alpha']) {
  const item = candidates.get(`native-${variant}`)
  if(!item)continue
  await t.test(`native-${variant}.js: actual buildSessionServices preserves Auto/native routing boundary`, () => {
   replayNativeRoutingAndFallback(item.source, item.filename, item.aliases)
  })
 }
})
