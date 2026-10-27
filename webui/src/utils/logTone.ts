/** 日志行的级别着色：按前缀符号判定，与后端 `[模块] ✓/✗/○ 消息` 的约定对齐（日志页与插件日志共用） */
export function logToneOf(line: string) {
  if (line.includes('✗') || /ERROR|error|失败|错误/.test(line)) return 'err'
  if (line.includes('✓') || /成功|完成/.test(line)) return 'ok'
  if (line.includes('⚠') || /WARN|warn|警告/.test(line)) return 'warn'
  if (line.includes('○')) return 'dim'
  return ''
}
