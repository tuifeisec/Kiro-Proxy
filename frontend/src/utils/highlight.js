// JSON 语法高亮：先转义 HTML 特殊字符，再按 token 类型（键 / 字符串 / 数字 / 布尔 / null）
// 包裹 Tailwind 颜色类，供 v-html 渲染。
// 输入应为 JSON.stringify 的产物（或同样可信的 JSON 文本），特殊字符已在函数内转义。
// 颜色与 BodyViewer / LogDetail 原有高亮一致：键 #2563eb、字符串 #059669、
// 数字 #d97706、布尔 #7c3aed、null #6b7280。
export function highlightJSON(json) {
  return json
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(
      /("(\\u[a-zA-Z0-9]{4}|\\[^u]|[^\\"])*"(\s*:)?|\b(true|false|null)\b|-?\d+(?:\.\d*)?(?:[eE][+-]?\d+)?)/g,
      (match) => {
        let cls = 'text-[#059669]'
        if (/^"/.test(match)) {
          if (/:$/.test(match)) cls = 'text-[#2563eb]'
        } else if (/true|false/.test(match)) {
          cls = 'text-[#7c3aed]'
        } else if (/null/.test(match)) {
          cls = 'text-[#6b7280]'
        } else {
          cls = 'text-[#d97706]'
        }
        return `<span class="${cls}">${match}</span>`
      }
    )
}
