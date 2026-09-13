import { onScopeDispose, ref, watch } from 'vue'

// 数值平滑 composable：返回一个以指数缓动趋近 source 的响应式 float ref。
//
// - source：number 型 ref/computed/getter；null/undefined/NaN/Infinity 等非有限输入统一按 0 处理。
// - options.tauMs：指数缓动时间常数，默认 340ms，越小跟随越快。
// - options.epsilon：收敛阈值下限，默认 0.5；实际阈值为 max(epsilon, |target| * 0.1%)。
// - options.snapFirstChange：为 true 时把首次变化也视作首值直接落位（覆盖
//   “loading 占位 0 → 首个真实数据”的冷启动场景），后续变化才做平滑。
// - 帧率无关：每帧推进 current + (target - current) * (1 - exp(-dt / tau))，
//   dt 为真实帧间隔并钳制到 64ms，避免后台标签页切回时因超长 dt 瞬移。
// - 静止零开销：进入收敛阈值即精确置为 target 并停止 rAF；source 不变化时不运行循环。
// - 首值不动画：setup 时同步采用 source 当前值，初始挂载不滚数字。
// - 返回原始 float ref，整数取整与格式化（千分位、单位缩写、小数位）由调用方负责。
export function useSmoothNumber(source, options = {}) {
  const tauMs = Number(options.tauMs) > 0 ? Number(options.tauMs) : 340
  const epsilonFloor = Number(options.epsilon) > 0 ? Number(options.epsilon) : 0.5
  const snapFirstChange = options.snapFirstChange === true
  const maxDtMs = 64

  const getSource = typeof source === 'function' ? source : () => source.value
  const sanitize = (value) => {
    const number = Number(value)
    return Number.isFinite(number) ? number : 0
  }

  const output = ref(sanitize(getSource()))
  let target = output.value
  let rafId = 0
  let lastTime = 0
  let firstChangeSeen = false

  function settleEpsilon() {
    return Math.max(epsilonFloor, Math.abs(target) * 0.001)
  }

  function stopLoop() {
    if (rafId) cancelAnimationFrame(rafId)
    rafId = 0
  }

  function tick(now) {
    rafId = 0
    const dtMs = Math.min(maxDtMs, Math.max(0, now - lastTime))
    lastTime = now
    output.value += (target - output.value) * (1 - Math.exp(-dtMs / tauMs))
    if (Math.abs(target - output.value) <= settleEpsilon()) {
      output.value = target
      return
    }
    rafId = requestAnimationFrame(tick)
  }

  watch(() => sanitize(getSource()), (next) => {
    target = next
    if (snapFirstChange && !firstChangeSeen) {
      firstChangeSeen = true
      stopLoop()
      output.value = target
      return
    }
    if (Math.abs(target - output.value) <= settleEpsilon()) {
      // 已在收敛阈值内（含微小变化），直接落位，不启动循环。
      stopLoop()
      output.value = target
      return
    }
    if (!rafId) {
      lastTime = performance.now()
      rafId = requestAnimationFrame(tick)
    }
  })

  onScopeDispose(stopLoop)

  return output
}
