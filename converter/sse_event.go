package converter

import (
	"bufio"
	"strings"
)

// readSSEEvent 只返回由空行结束的完整事件；EOF 不隐式补齐事件。
// 终态也经过同一边界，避免转换器先关闭源流而观测器尚未解析最终用量。
func readSSEEvent(reader *bufio.Reader) (eventType, data string, err error) {
	var lines []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", "", err
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			if len(lines) > 0 {
				return eventType, strings.Join(lines, "\n"), nil
			}
			eventType = ""
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			eventType = value
		case "data":
			lines = append(lines, value)
		}
	}
}
