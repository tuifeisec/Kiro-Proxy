const statusText = {
  legacy: '旧记录缺少测速用量与精确时间，无法按新口径计算',
  unmeasured: '未观测到流式内容区间，无法计算解码速度',
  failed: '请求失败，不参与测速',
  incomplete: '内容观测不完整，不参与测速',
  no_content: '没有可识别的正文或工具输出',
  usage_missing: '上游用量缺失、缺少必要细分，或包含无法对应的非文本输出',
  insufficient_tokens: '测速 Token 不足两个，无法计算首 Token 之后的速度',
  insufficient_interval: '输出合并到同一次读取或缺少有效的时间区间',
  invalid_interval: '测速区间超出请求耗时，数据无效',
}

export function tokenSpeedExplanation(item) {
  if (item.token_rate_status !== 'ok') {
    return statusText[item.token_rate_status] || '缺少有效测速数据'
  }
  const basis = item.speed_basis === 'output_with_reasoning'
    ? '包含可观测推理'
    : item.speed_basis === 'reported_output'
      ? '按上游声明的输出量，未提供推理细分'
      : '排除隐藏推理'
  const duration = Number(item.speed_duration_ns) / 1e6
  return `(${item.speed_output_tokens} − 1) Token ÷ ${duration.toFixed(3)} ms；${basis}。代理按内容分块到达计时，首块可能含多个 Token，与上游内部测速可能有差异。`
}
