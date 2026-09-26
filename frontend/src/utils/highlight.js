// JSON 语法高亮：先转义 HTML 特殊字符，再按 token 类型（键 / 字符串 / 数字 / 布尔 / null）
// 包裹语义 code token 颜色类（code-key / code-string / code-number / code-bool / code-null，
// 暗色主题下由 style.css 的 .dark 令牌自动切换为亮色变体），供 v-html 渲染。
// 输入应为 JSON.stringify 的产物（或同样可信的 JSON 文本），特殊字符已在函数内转义。
export function highlightJSON(json) {
  return json
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(
      /("(\\u[a-zA-Z0-9]{4}|\\[^u]|[^\\"])*"(\s*:)?|\b(true|false|null)\b|-?\d+(?:\.\d*)?(?:[eE][+-]?\d+)?)/g,
      (match) => {
        let cls = 'text-code-string'
        if (/^"/.test(match)) {
          if (/:$/.test(match)) cls = 'text-code-key'
        } else if (/true|false/.test(match)) {
          cls = 'text-code-bool'
        } else if (/null/.test(match)) {
          cls = 'text-code-null'
        } else {
          cls = 'text-code-number'
        }
        return `<span class="${cls}">${match}</span>`
      }
    )
}
