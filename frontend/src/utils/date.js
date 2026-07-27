export const ALL_STATS_START_DATE = '0001-01-01'

function formatLocalDate(date) {
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

export function daysAgo(days = 0) {
  const date = new Date()
  date.setDate(date.getDate() - days)
  return formatLocalDate(date)
}

export function statsDaysAgo(days = 0) {
  const date = new Date()
  if (date.getHours() < 6) {
    date.setDate(date.getDate() - 1)
  }
  date.setDate(date.getDate() - days)
  return formatLocalDate(date)
}
