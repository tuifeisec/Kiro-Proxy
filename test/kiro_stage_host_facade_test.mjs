import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import test from 'node:test'
import { createContext, Script } from 'node:vm'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { PATCHES } from '../tools/kiro_stage_bridge.mjs'

const frontendRequire = createRequire(new URL('../frontend/package.json', import.meta.url))
const babelParser = frontendRequire('@babel/parser')
const nodeRequire = createRequire(import.meta.url)
const families = ['legacy', 'native']
const variants = ['original', 'alpha']
const manifestFiles = [
 '../proxy/kiro_stage_bridge_manifest.json',
 '../proxy/kiro_stage_bridge_130_manifest.json',
]

function parse(source, filename) {
 return babelParser.parse(source, {
  sourceType: 'unambiguous',
  allowReturnOutsideFunction: true,
  allowAwaitOutsideFunction: true,
  sourceFilename: filename,
 })
}

function visit(node, visitor, parent = undefined) {
 if (!node || typeof node !== 'object') return
 if (typeof node.type === 'string') visitor(node, parent)
 for (const [key, value] of Object.entries(node)) {
  if (['loc', 'start', 'end', 'extra', 'leadingComments', 'innerComments', 'trailingComments'].includes(key)) continue
  if (Array.isArray(value)) for (const child of value) visit(child, visitor, node)
  else if (value && typeof value === 'object') visit(value, visitor, node)
 }
}

function patchNamed(name) {
 const matches = PATCHES.filter(([patchName]) => patchName === name)
 assert.equal(matches.length, 1, `PATCHES must define exactly one ${name} patch`)
 return matches[0]
}

function containsCall(node, actualName) {
 let found = false
 visit(node, current => {
  if (current.type === 'CallExpression' && current.callee?.type === 'Identifier' && current.callee.name === actualName) found = true
 })
 return found
}

test('workflow source uses the restricted host route accessor in both manifests', () => {
 const [, , workflowAfter] = patchNamed('workflow-source')
 assert.match(workflowAfter, /getSessionKiroProxyRoute\s*\(/)
 assert.doesNotMatch(workflowAfter, /\.getSession\s*\(/)
 assert.match(workflowAfter, /taskType\s*:\s*[\w$]+\.taskType\s*\?\?\s*""/)
 assert.match(workflowAfter, /parentRoute\s*:\s*[\w$]+\.host\.getSessionKiroProxyRoute\([\w$]+\)\s*\?\?\s*\{\}/)

 const facadePatches = PATCHES.filter(([, , after]) => after.includes('getSessionKiroProxyRoute:c('))
 assert.equal(facadePatches.length, 1, 'PATCHES must add one getSessionKiroProxyRoute host-facade accessor')
 assert.match(facadePatches[0][2], /getSessionKiroProxyRoute:c\([^,]+=>this\.host\.getSession\([^)]*\)\?\.kiroProxyRoute,"getSessionKiroProxyRoute"\)/)

 for (const relativePath of manifestFiles) {
  const manifestPath = new URL(relativePath, import.meta.url)
  const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'))
  const workflow = manifest.patches.find(patch => patch.name === 'workflow-source')
  const facade = manifest.patches.find(patch => patch.name === 'workflow-host-route-accessor')
  assert.ok(workflow, `${manifestPath.pathname} omits workflow-source patch`)
  assert.ok(facade, `${manifestPath.pathname} omits workflow-host-route-accessor patch`)
  assert.match(workflow.after, /getSessionKiroProxyRoute\(/)
  assert.doesNotMatch(workflow.after, /\.getSession\(/)
  assert.match(workflow.after, /parentRoute:[\w$]+\.host\.getSessionKiroProxyRoute\([\w$]+\)\?\?\{\}/)
  assert.match(facade.after, /getSessionKiroProxyRoute:c\([^,]+=>this\.host\.getSession\([^)]*\)\?\.kiroProxyRoute,"getSessionKiroProxyRoute"\)/)
  const patch = PATCHES.find(([name]) => name === 'workflow-host-route-accessor')
  assert.ok(patch, 'PATCHES must define the workflow host route accessor')
  assert.equal(facade.before, patch[1], `${manifestPath.pathname}: stale route accessor anchor`)
  assert.equal(facade.after, patch[2], `${manifestPath.pathname}: stale route accessor implementation`)
 }
})

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

function aliasMap(aliases) {
 const originalByActual = new Map()
 for (const [key, actual] of Object.entries(aliases)) {
  const split = key.lastIndexOf('@')
  assert.ok(split > 0 && /^\d+$/.test(key.slice(split + 1)), `invalid alias key ${key}`)
  assert.match(actual, /^kx\d+$/, `alpha binding alias is not offset-derived: ${actual}`)
  assert.equal(originalByActual.has(actual), false, `duplicate alpha binding alias ${actual}`)
  originalByActual.set(actual, key.slice(0, split))
 }
 return originalByActual
}

function semanticName(actual, originalByActual) {
 return originalByActual.get(actual) ?? actual
}

function extractedBridge(source, ast, originalByActual, filename) {
 const fragments = []
 const exposed = new Map()
 const add = (canonical, actual) => {
  assert.equal(exposed.has(canonical), false, `duplicate bridge binding ${canonical}`)
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
  for (const declaration of node.declarations) {
   const names = bindingNames(declaration.id)
   const bridgeNames = names.filter(name => semanticName(name, originalByActual).startsWith('kiroProxy'))
   if (!bridgeNames.length) continue
   selected.push(source.slice(declaration.start, declaration.end))
   for (const actual of bridgeNames) add(semanticName(actual, originalByActual), actual)
  }
  if (selected.length) fragments.push({ start: node.start, source: `${node.kind} ${selected.join(',')};` })
 }
 for (const name of ['kiroProxyAgentRevisionV2', 'kiroProxyWriteSourceV2', 'kiroProxyReadSourceV2', 'kiroProxyStepRouteV2', 'kiroProxyDefinitionV2']) {
  assert.ok(exposed.has(name), `${filename}: actual bridge candidate omitted ${name}`)
 }
 const bridgeProperties = [...exposed].map(([canonical, actual]) => `${JSON.stringify(canonical)}:${actual}`).join(',')
 fragments.sort((left, right) => left.start - right.start)
 const code = `${fragments.map(fragment => fragment.source).join('\n')}\nglobalThis.__kiroBridge={${bridgeProperties}};`
 const context = createContext({
  Buffer,
  process: { env: { KIRO_PROXY_STAGE_BRIDGE: '1' } },
  require: specifier => {
   assert.ok(['node:path', 'node:crypto', 'path', 'crypto'].includes(specifier), `${filename}: unexpected helper module ${specifier}`)
   return nodeRequire(specifier.startsWith('node:') ? specifier : `node:${specifier}`)
  },
 })
 for (const node of topLevelNodes(ast)) {
  if (node.type !== 'VariableDeclaration') continue
  for (const declaration of node.declarations) {
   const specifiers = new Set()
   visit(declaration.init, current => {
    if (current.type === 'CallExpression' && current.callee?.type === 'Identifier' && current.callee.name === 'require' && current.arguments?.[0]?.type === 'StringLiteral') specifiers.add(current.arguments[0].value)
   })
   if (specifiers.size !== 1) continue
   const specifier = [...specifiers][0]
   if (['node:path', 'node:crypto', 'path', 'crypto'].includes(specifier)) {
    for (const actual of bindingNames(declaration.id)) context[actual] = context.require(specifier)
   }
  }
 }
 new Script(code, { filename: `${filename}#actual-bridge` }).runInContext(context)
 return { bridge: context.__kiroBridge, exposed }
}

function findFacadeAccessor(source, ast) {
 const matches = []
 visit(ast.program, node => {
  if (node.type === 'ObjectProperty' && node.key?.type === 'Identifier' && node.key.name === 'getSessionKiroProxyRoute') matches.push(node)
 })
 assert.equal(matches.length, 1, 'candidate must contain one actual restricted host facade route accessor')
 const property = matches[0]
 assert.equal(property.value.type, 'CallExpression', 'route accessor must pass through the actual host facade wrapper')
 assert.equal(property.value.arguments?.[1]?.value, 'getSessionKiroProxyRoute')
 const wrapperName = identifierOf(property.value.callee)
 assert.ok(wrapperName, 'host facade wrapper must be an identifier call discovered from the candidate AST')
 const factory = new Function(wrapperName, `return function(){return ${source.slice(property.value.start, property.value.end)}}`)(
  (callback, name) => {
   assert.equal(name, 'getSessionKiroProxyRoute')
   assert.equal(typeof callback, 'function')
   return callback
  },
 )
 return factory
}

function findWorkflowSourceIf(source, ast, writeHelperName) {
 const matches = []
 visit(ast.program, node => {
  if (node.type !== 'IfStatement') return
  if (!containsCall(node.consequent, writeHelperName)) return
  let routeCall
  visit(node.consequent, current => {
   if (current.type === 'CallExpression' && current.callee?.type === 'MemberExpression' && current.callee.property?.name === 'getSessionKiroProxyRoute') routeCall = current
  })
  if (routeCall) matches.push({ node, routeCall })
 })
 assert.equal(matches.length, 1, 'candidate must contain one actual workflow source write using the restricted host route accessor')
 return { ...matches[0], source: source.slice(matches[0].node.start, matches[0].node.end) }
}

function referencedIdentifiers(node) {
 const declared = new Set()
 const referenced = new Set()
 visit(node, (current, parent) => {
  if (current.type === 'VariableDeclarator') for (const name of bindingNames(current.id)) declared.add(name)
  if ((current.type === 'FunctionDeclaration' || current.type === 'FunctionExpression' || current.type === 'ArrowFunctionExpression')) {
   if (current.id) declared.add(current.id.name)
   for (const parameter of current.params) for (const name of bindingNames(parameter)) declared.add(name)
  }
  if (current.type === 'CatchClause') for (const name of bindingNames(current.param)) declared.add(name)
  if (current.type !== 'Identifier') return
  if ((parent?.type === 'MemberExpression' || parent?.type === 'OptionalMemberExpression') && parent.property === current && !parent.computed) return
  if ((parent?.type === 'ObjectProperty' || parent?.type === 'ObjectMethod') && parent.key === current && !parent.computed && !parent.shorthand) return
  if ((parent?.type === 'VariableDeclarator' && parent.id === current) || (parent?.type === 'FunctionDeclaration' && parent.id === current)) return
  if ((parent?.type === 'FunctionDeclaration' || parent?.type === 'FunctionExpression' || parent?.type === 'ArrowFunctionExpression') && parent.params.includes(current)) return
  if (parent?.type === 'LabeledStatement' && parent.label === current) return
  referenced.add(current.name)
 })
 return [...referenced].filter(name => !declared.has(name))
}

function identifierOf(node) {
 return node?.type === 'Identifier' ? node.name : undefined
}

function plain(value) {
 return value === undefined ? undefined : JSON.parse(JSON.stringify(value))
}

function collectCalls(node, predicate) {
 const matches = []
 visit(node, current => {
  if (current.type === 'CallExpression' && predicate(current)) matches.push(current)
 })
 return matches
}

function workflowInputs(candidateSource, candidateAst, bridge, facadeFactory, taskType, parentRoute, bridgeEnabled = '1', modelId = 'auto') {
 const { node, routeCall, source: sourceCode } = findWorkflowSourceIf(candidateSource, candidateAst, bridge.actualWrite)
 const writeCalls = collectCalls(node.consequent, call => call.callee?.type === 'Identifier' && call.callee.name === bridge.actualWrite)
 assert.equal(writeCalls.length, 1, 'workflow source must call the actual bridge sidecar writer once')
 assert.equal(routeCall.arguments.length, 1, 'workflow source must look up exactly the parent session identifier')
 const sourceFile = parse(sourceCode, 'workflow-source-patch.js')
 const workflowPathNames = new Set()
 visit(sourceFile, current => {
  if (current.type === 'MemberExpression' && current.property?.type === 'Identifier' && current.property.name === 'workflowPath') {
   const name = identifierOf(current.object)
   if (name) workflowPathNames.add(name)
  }
 })
 assert.equal(workflowPathNames.size, 1, 'workflow source must read one task input object')
 const taskInputName = [...workflowPathNames][0]
 const facadeOwners = new Set()
 visit(sourceFile, current => {
  if (current.type !== 'MemberExpression' || current.property?.name !== 'getSessionKiroProxyRoute' || current.object?.type !== 'MemberExpression') return
  const hostMember = current.object
  if (hostMember.property?.name === 'host') {
   const owner = identifierOf(hostMember.object)
   if (owner) facadeOwners.add(owner)
  }
 })
 assert.equal(facadeOwners.size, 1, 'workflow source must use one host adapter value')
 const modeNames = new Set()
 visit(node.test, current => {
  if (current.type !== 'BinaryExpression' || current.operator !== '===' && current.operator !== '==') return
  for (const [candidate, literal] of [[current.left, current.right], [current.right, current.left]]) {
   if ((literal?.type === 'StringLiteral' || literal?.type === 'Literal') && literal.value === 'auto') {
    const name = identifierOf(candidate)
    if (name) modeNames.add(name)
   }
  }
 })
 assert.equal(modeNames.size, 1, 'workflow source gate must compare one resolved model value to Auto')
 const modeName = [...modeNames][0]
 const routeSessionNames = bindingNames(routeCall.arguments[0])
 assert.ok(routeSessionNames.length, 'parent session lookup must receive a value from the enclosing workflow handler')

 const writer = writeCalls[0]
 assert.equal(writer.arguments.length, 4, 'workflow source must use the four-argument actual sidecar helper')
 const baseName = identifierOf(writer.arguments[1])
 const workflowIdName = identifierOf(writer.arguments[2])
 assert.ok(baseName, 'sidecar helper base path must be a workflow handler local')
 assert.ok(workflowIdName, 'sidecar helper workflow id must be a workflow handler local')

 const fs = mockFs()
 const parentSessionId = 'mock-parent-session'
 let sessionLookups = 0
 const adapterThis = {
  host: {
   getSession(sessionId) {
    sessionLookups++
    assert.equal(sessionId, parentSessionId)
    return parentRoute === undefined ? undefined : { kiroProxyRoute: parentRoute }
   },
  },
 }
 const facade = facadeFactory.call(adapterThis)
 assert.equal(typeof facade, 'function', 'actual host adapter accessor must produce a callable facade method')
 const restrictedHost = { getSessionKiroProxyRoute: facade, stateFs: fs }
 assert.equal(Object.hasOwn(restrictedHost, 'getSession'), false, 'workflow handler facade must not expose getSession')
 const handlerHostName = identifierOf(routeCall.callee.object.object)

 const canonicalByActual = new Map([...bridge.exposed].map(([canonical, actual]) => [actual, canonical]))
 const available = new Map()
 const refs = referencedIdentifiers(node)
 const workflow = { name: 'agent-facade-test', steps: [{ type: 'step', id: 'run', agent: 'worker' }] }
 const workflowNames = new Set()
 for (const helperName of [bridge.exposed.get('kiroProxyAgentRevisionV2'), bridge.exposed.get('kiroProxyDefinitionV2')]) {
  visit(node, current => {
   if (current.type !== 'CallExpression' || current.callee?.type !== 'Identifier' || current.callee.name !== helperName) return
   const name = identifierOf(current.arguments?.[0])
   if (name) workflowNames.add(name)
  })
 }
 for (const name of refs) {
  const canonical = canonicalByActual.get(name)
  if (canonical?.startsWith('kiroProxyGlobalV3_')) {
   const globalName = canonical.slice('kiroProxyGlobalV3_'.length)
   if (globalName === 'process') {
    available.set(name, { env: { KIRO_PROXY_STAGE_BRIDGE: bridgeEnabled } })
    continue
   }
   assert.ok(globalName in globalThis, `candidate uses unsupported bridge global ${globalName}`)
   available.set(name, globalThis[globalName])
   continue
  }
  if (canonical) {
   assert.ok(bridge.runtime[canonical], `actual helper ${canonical} is unavailable`)
   available.set(name, bridge.runtime[canonical])
   continue
  }
  if (name === 'process') {
   available.set(name, { env: { KIRO_PROXY_STAGE_BRIDGE: bridgeEnabled } })
   continue
  }
  if (name === handlerHostName) {
   available.set(name, { host: restrictedHost, stateFs: fs })
   continue
  }
  if (name === taskInputName) {
   available.set(name, { workflowPath: 'agent://worker', ...(taskType === undefined ? {} : { taskType }) })
   continue
  }
  if (name === modeName) {
   available.set(name, modelId)
   continue
  }
  if (routeSessionNames.includes(name)) {
   available.set(name, parentSessionId)
   continue
  }
  if (name === baseName) {
   available.set(name, 'mock-runs/workflows')
   continue
  }
  if (name === workflowIdName) {
   available.set(name, 'child-facade-workflow')
   continue
  }
  if (workflowNames.has(name)) {
   available.set(name, workflow)
   continue
  }
  if (name in globalThis) {
   available.set(name, globalThis[name])
   continue
  }
  assert.fail(`cannot bind actual workflow source free identifier ${name}`)
 }
 const params = [...available.keys()]
 const values = [...available.values()]
 const execute = new Function(...params, `return (async()=>{${sourceCode}\n})()`)
 return {
  execute: () => execute(...values),
  fs,
  base: available.get(baseName),
  workflowId: available.get(workflowIdName),
  parentRoute,
  parentSessionId,
  getSessionLookups: () => sessionLookups,
 }
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

async function replayCandidate(source, filename, aliases) {
 const ast = parse(source, filename)
 const originalByActual = aliasMap(aliases)
 const extracted = extractedBridge(source, ast, originalByActual, filename)
 const bridgeRuntime = {
  kiroProxyAgentRevisionV2: extracted.bridge.kiroProxyAgentRevisionV2,
  kiroProxyWriteSourceV2: extracted.bridge.kiroProxyWriteSourceV2,
  kiroProxyReadSourceV2: extracted.bridge.kiroProxyReadSourceV2,
  kiroProxyStepRouteV2: extracted.bridge.kiroProxyStepRouteV2,
  kiroProxyDefinitionV2: extracted.bridge.kiroProxyDefinitionV2,
 }
 const bridge = {
  runtime: bridgeRuntime,
  exposed: extracted.exposed,
  actualWrite: extracted.exposed.get('kiroProxyWriteSourceV2'),
 }
 const facadeFactory = findFacadeAccessor(source, ast)

 for (const scenario of [
  { name: 'inherit complete parent route', taskType: undefined, parentRoute: {
   nodeId: 'parent-node', definitionId: 'bundled://parent', definitionRevision: 'parent-rev',
   nodePath: 'plan/review', taskType: 'code_review', executionId: 'parent-run', workflowId: 'parent-workflow',
  } },
  { name: 'explicit task type without parent route', taskType: 'investigation', parentRoute: undefined },
  { name: 'omitted task type without parent route', taskType: undefined, parentRoute: undefined },
 ]) {
  const inputs = workflowInputs(source, ast, bridge, facadeFactory, scenario.taskType, scenario.parentRoute)
  const originalParent = scenario.parentRoute === undefined ? undefined : JSON.parse(JSON.stringify(scenario.parentRoute))
  await inputs.execute()
  assert.equal(inputs.getSessionLookups(), 1, `${filename} (${scenario.name}) must use the facade accessor once`)
  assert.deepEqual(scenario.parentRoute, originalParent, `${filename} (${scenario.name}) must not mutate the parent route`)
  const record = await bridgeRuntime.kiroProxyReadSourceV2(inputs.fs, inputs.base, inputs.workflowId)
  assert.ok(record, `${filename} (${scenario.name}) must persist source through the actual helper`)
  if (scenario.parentRoute) {
   assert.deepEqual(plain(record.parentRoute), scenario.parentRoute, `${filename} (${scenario.name}) must persist the complete parent route`)
   assert.deepEqual(scenario.parentRoute, originalParent)
   const inherited = bridgeRuntime.kiroProxyStepRouteV2({
    workflowId: inputs.workflowId,
    nodeId: 'run',
    nodePath: [inputs.workflowId, 'run'],
    kiroProxyDefinition: { name: 'agent-facade-test', steps: [{ type: 'step', id: 'run', agent: 'worker' }] },
   }, 'child-session', 0)
   assert.equal(inherited.taskType, scenario.parentRoute.taskType)
   assert.equal(inherited.definitionId, scenario.parentRoute.definitionId)
   assert.equal(inherited.executionId, scenario.parentRoute.executionId)
   assert.equal(inherited.workflowId, inputs.workflowId)
  } else if (scenario.taskType) {
   assert.equal(record.taskType, scenario.taskType)
   assert.deepEqual(plain(record.parentRoute), {})
   const explicit = bridgeRuntime.kiroProxyStepRouteV2({
    workflowId: inputs.workflowId,
    nodeId: 'run',
    nodePath: [inputs.workflowId, 'run'],
    kiroProxyDefinition: { name: 'agent-facade-test', steps: [{ type: 'step', id: 'run', agent: 'worker' }] },
   }, 'child-session', 0)
   assert.equal(explicit.taskType, scenario.taskType)
   assert.equal(explicit.executionId, 'child-session')
  } else {
   assert.equal(record.definitionId, '')
   assert.equal(record.definitionRevision, '')
   assert.equal(record.taskType, '')
   assert.deepEqual(plain(record.parentRoute), {})
   const emptyAgent = bridgeRuntime.kiroProxyStepRouteV2({
    workflowId: inputs.workflowId,
    nodeId: 'run',
    nodePath: [inputs.workflowId, 'run'],
    kiroProxyDefinition: { name: 'agent-facade-test', steps: [{ type: 'step', id: 'run', agent: 'worker' }] },
   }, 'child-session', 0)
   assert.deepEqual(plain(emptyAgent), { executionId: '', workflowId: inputs.workflowId })
  }
 }

 for (const disabled of [
  { env: '0', model: 'auto', name: 'bridge off' },
  { env: '1', model: 'manual-model', name: 'non-auto' },
 ]) {
  const inputs = workflowInputs(source, ast, bridge, facadeFactory, 'investigation', undefined, disabled.env, disabled.model)
  await inputs.execute()
  assert.equal(inputs.fs.files.size, 0, `${filename} (${disabled.name}) must not write workflow source`)
  assert.equal(inputs.getSessionLookups(), 0, `${filename} (${disabled.name}) must not query parent route`)
 }
}

function candidateSpecs(outputDirectory) {
 const standard = families.flatMap(family => variants.map(variant => ({
  key: `${family}-${variant}`,
  family,
  variant,
  path: path.join(outputDirectory, `${family}-${variant}.js`),
 })))
 if (standard.every(candidate => existsSync(candidate.path))) return standard

 const generated = []
 for (const [family, prefix] of [['legacy', 'legacy-workflow'], ['native', 'native-model-routing']]) {
  for (const variant of ['manifest-candidate', 'profile-candidate']) {
   const candidatePath = path.join(outputDirectory, `${prefix}-${variant}.js`)
   if (existsSync(candidatePath)) generated.push({ key: `${family}-${variant}`, family, variant, path: candidatePath })
  }
 }
 assert.equal(generated.length, 4, 'candidate directory must contain both patched candidate forms for legacy and native source families')
 return generated
}

test('compiled adapter facade and workflow handler preserve parent route for each source family', async t => {
 const outputDirectory = process.env.KIRO_STAGE_AUTO_TEST_OUTPUT
 if (!outputDirectory) {
  t.skip('KIRO_STAGE_AUTO_TEST_OUTPUT is not set; waiting for compiled candidate artifacts')
  return
 }
 assert.ok(existsSync(outputDirectory), `candidate output directory does not exist: ${outputDirectory}`)
 const candidates = candidateSpecs(outputDirectory)
 const selected = process.env.KIRO_STAGE_HOST_FACADE_TEST_VARIANT
 if (!selected) {
  for (const candidate of candidates) await t.test(candidate.key, () => {
   const childEnv = { ...process.env, KIRO_STAGE_HOST_FACADE_TEST_VARIANT: candidate.key }
   delete childEnv.NODE_TEST_CONTEXT
   const result = spawnSync(process.execPath, ['--test', '--test-reporter=tap', fileURLToPath(import.meta.url)], {
    env: childEnv,
    encoding: 'utf8',
    windowsHide: true,
    timeout: 120000,
    maxBuffer: 4 * 1024 * 1024,
   })
   assert.equal(result.error, undefined, result.error?.message)
   assert.equal(result.status, 0, result.stdout + '\n' + result.stderr)
   assert.match(result.stdout, /# tests 2[\s\S]*# pass 2[\s\S]*# fail 0[\s\S]*# skipped 0/, 'child process must execute both tests without failures or skips')
  })
  return
 }
 const candidate = candidates.find(item => item.key === selected)
 assert.ok(candidate, `selected candidate ${selected} is absent from ${outputDirectory}`)
 const source = readFileSync(candidate.path, 'utf8')
 const aliasesPath = path.join(outputDirectory, `${candidate.family}-aliases.json`)
 const aliases = candidate.variant === 'alpha' ? JSON.parse(readFileSync(aliasesPath, 'utf8')) : {}
 await replayCandidate(source, candidate.path, aliases)
})
