export const ALL_STATS_START_DATE = '0001-01-01'

function formatLocalDate(date) {
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

export function statsDaysAgo(days = 0) {
  // 统计日为自然日（本地 00:00 分界）
  const date = new Date()
  date.setDate(date.getDate() - days)
  return formatLocalDate(date)
}
