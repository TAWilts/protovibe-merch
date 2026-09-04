function escapeCsv(value: unknown): string {
  const text = String(value ?? '')
  return /[;"\r\n]/.test(text) ? `"${text.replace(/"/g, '""')}"` : text
}

export function downloadCsv(filename: string, header: string[], rows: unknown[][]) {
  const content = [
    header.map(escapeCsv).join(';'),
    ...rows.map((row) => row.map(escapeCsv).join(';')),
  ].join('\r\n')

  const blob = new Blob([`\ufeff${content}\r\n`], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  document.body.appendChild(link)
  link.click()
  link.remove()
  URL.revokeObjectURL(url)
}

export function datedFilename(base: string) {
  const now = new Date()
  const local = new Date(now.getTime() - now.getTimezoneOffset() * 60_000)
  return `${base}-${local.toISOString().slice(0, 10)}.csv`
}