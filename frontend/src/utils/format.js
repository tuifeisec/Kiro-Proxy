// 跨视图共享的数字和美元费用格式化函数。同一语义只保留一份实现；
// 请求统计（RequestUsage）的 formatNum 与 Token 汇总（formatTokens）
// 在尾零清理和千分位行为上有历史差异，为保持各页面显示不变而分别导出。
export function formatTokens(n) {
  const value = Number(n || 0)
  if (value >= 1_000_000_000) return (value / 1_000_000_000).toFixed(2).replace(/\.00$/, '') + 'B'
  if (value >= 1_000_000) return (value / 1_000_000).toFixed(2).replace(/\.00$/, '') + 'M'
  if (value >= 1_000) return (value / 1_000).toFixed(1).replace(/\.0$/, '') + 'K'
  return String(value)
}

export function formatNum(value) {
  const number = Number(value || 0)
  if (number >= 1_000_000_000) return `${(number / 1_000_000_000).toFixed(2)}B`
  if (number >= 1_000_000) return `${(number / 1_000_000).toFixed(2)}M`
  if (number >= 1_000) return `${(number / 1_000).toFixed(1)}K`
  return number.toLocaleString()
}

export function formatUsd(n) {
  return `$${Number(n || 0).toFixed(6)}`
}
