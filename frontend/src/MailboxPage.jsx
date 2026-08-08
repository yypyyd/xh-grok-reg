import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react'

const PAGE_SIZE = 20
const EMPTY_FORM = {
  email: '', password: '', provider: '', client_id: '', refresh_token: '',
  status: 'unverified', note: '',
}
const STATUS_TEXT = {
  unverified: '待验证',
  verifying: '验证中',
  verify_failed: '验证失败',
  verified: '已验证',
}

function formatTime(value) {
  return value ? new Date(value).toLocaleString('zh-CN') : '—'
}

function parseImport(text) {
  return text.split(/\r?\n/)
    .map(line => line.trim())
    .filter(Boolean)
    .map(line => line.split('----').map(part => part.trim()))
    .filter(parts => parts.length === 4 && parts[0].includes('@'))
    .map(parts => ({
      email: parts[0], password: parts[1], client_id: parts[2], refresh_token: parts[3],
    }))
}

function ImportModal({ api, notify, onClose, onImported }) {
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const items = useMemo(() => parseImport(text), [text])
  const readFile = file => {
    if (!file) return
    const reader = new FileReader()
    reader.onload = () => setText(String(reader.result || ''))
    reader.readAsText(file)
  }
  const submit = async () => {
    if (!items.length) { notify('没有可导入的有效行', 'error'); return }
    setBusy(true)
    try {
      const result = await api.post('/api/mailboxes/import', { items })
      notify(`识别 ${items.length} 个：新增 ${result.added || 0}，跳过 ${result.skipped || 0}；已进入认证队列 ${result.queued || 0} 个`)
      onImported()
      onClose()
    } catch (error) { notify(error.message, 'error') }
    finally { setBusy(false) }
  }
  return <div className="modal-backdrop" onMouseDown={event => event.target === event.currentTarget && onClose()}>
    <div className="modal wide-modal import-modal">
      <h4>批量导入邮箱</h4>
      <p className="muted">每行一个邮箱。重复邮箱会自动跳过，导入成功后由后台认证队列继续处理。</p>
      <code className="import-format">someone@outlook.com----密码----client_id----refresh_token</code>
      <textarea value={text} onChange={event => setText(event.target.value)} onDragOver={event => event.preventDefault()} onDrop={event => { event.preventDefault(); readFile(event.dataTransfer.files?.[0]) }} placeholder="粘贴邮箱数据，或把 txt 文件拖到这里…" rows="11" />
      <div className="import-footer">
        <label className="file-picker">选择 txt 文件<input type="file" accept=".txt,text/plain" onChange={event => readFile(event.target.files?.[0])} /></label>
        <span className="muted">已识别 {items.length} 个邮箱</span>
        <div className="modal-actions"><button className="secondary" onClick={onClose}>取消</button><button className="primary" onClick={submit} disabled={busy}>{busy ? '导入中…' : '导入并认证'}</button></div>
      </div>
    </div>
  </div>
}

function EditorModal({ api, mailbox, notify, onClose, onSaved }) {
  const [form, setForm] = useState(mailbox ? {
    email: mailbox.email || '', password: mailbox.password || '', provider: mailbox.provider || '',
    client_id: mailbox.client_id || '', refresh_token: mailbox.refresh_token || '',
    status: mailbox.status || 'unverified', note: mailbox.note || '',
  } : EMPTY_FORM)
  const [busy, setBusy] = useState(false)
  const set = (key, value) => setForm(current => ({ ...current, [key]: value }))
  const submit = async event => {
    event.preventDefault()
    if (!form.email.trim()) { notify('邮箱地址不能为空', 'error'); return }
    setBusy(true)
    try {
      if (mailbox) await api.put(`/api/mailboxes/${mailbox.id}`, { ...form, email: form.email.trim() })
      else await api.post('/api/mailboxes', { ...form, email: form.email.trim() })
      notify(mailbox ? '邮箱资料已更新' : '邮箱已添加并等待认证')
      onSaved()
      onClose()
    } catch (error) { notify(error.message, 'error') }
    finally { setBusy(false) }
  }
  return <div className="modal-backdrop" onMouseDown={event => event.target === event.currentTarget && onClose()}>
    <form className="modal wide-modal mailbox-editor" onSubmit={submit}>
      <div className="modal-title-row"><div><p className="eyebrow">MAILBOX CREDENTIALS</p><h4>{mailbox ? `编辑邮箱 #${mailbox.id}` : '添加邮箱'}</h4></div><button type="button" className="icon-btn" onClick={onClose}>×</button></div>
      <div className="form-grid">
        <label>邮箱地址 *<input required value={form.email} onChange={event => set('email', event.target.value)} /></label>
        <label>服务商<input value={form.provider} onChange={event => set('provider', event.target.value)} placeholder="outlook / gmail / imap" /></label>
        <label>邮箱密码<input type="password" value={form.password} onChange={event => set('password', event.target.value)} /></label>
        <label>认证状态<select value={form.status} onChange={event => set('status', event.target.value)}>{Object.entries(STATUS_TEXT).map(([value, label]) => <option value={value} key={value}>{label}</option>)}</select></label>
        <label className="full">Client ID（Outlook 取件）<input value={form.client_id} onChange={event => set('client_id', event.target.value)} /></label>
        <label className="full">Refresh Token（Outlook 取件）<textarea rows="4" value={form.refresh_token} onChange={event => set('refresh_token', event.target.value)} /></label>
        <label className="full">备注<textarea rows="2" value={form.note} onChange={event => set('note', event.target.value)} /></label>
      </div>
      <div className="modal-actions"><button type="button" className="secondary" onClick={onClose}>取消</button><button className="primary" disabled={busy}>{busy ? '保存中…' : '保存邮箱'}</button></div>
    </form>
  </div>
}

function messageKey(message) {
  return message?.id || `${message?.subject || ''}|${message?.received_at || ''}`
}

function mergeMessages(existing, incoming) {
  const map = new Map()
  existing.forEach(message => map.set(messageKey(message), message))
  incoming.forEach(message => map.set(messageKey(message), message))
  return [...map.values()].sort((a, b) => new Date(b.received_at || 0) - new Date(a.received_at || 0))
}

function InboxModal({ api, mailbox, onClose }) {
  const [messages, setMessages] = useState([])
  const [selectedID, setSelectedID] = useState('')
  const [body, setBody] = useState(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const fetching = useRef(false)
  const bodyCache = useRef(new Map())
  const selected = messages.find(message => messageKey(message) === selectedID) || messages[0]

  const fetchMessages = useCallback(async () => {
    if (fetching.current) return
    fetching.current = true
    try {
      const result = await api.get(`/api/mailboxes/${mailbox.id}/messages?limit=20`)
      setMessages(current => {
        const merged = mergeMessages(current, result.items || [])
        setSelectedID(active => active && merged.some(message => messageKey(message) === active) ? active : messageKey(merged[0]))
        return merged
      })
      setError('')
    } catch (requestError) { setError(requestError.message) }
    finally { fetching.current = false; setLoading(false) }
  }, [api, mailbox.id])

  useEffect(() => {
    fetchMessages()
    const timer = setInterval(() => { if (!document.hidden) fetchMessages() }, 3000)
    return () => clearInterval(timer)
  }, [fetchMessages])

  useEffect(() => {
    if (!selected) { setBody(null); return }
    const key = messageKey(selected)
    const cached = bodyCache.current.get(key)
    if (cached) { setBody(cached); return }
    let active = true
    setBody(null)
    api.get(`/api/mailboxes/${mailbox.id}/message?mid=${encodeURIComponent(selected.id)}`)
      .then(result => { if (active) { bodyCache.current.set(key, result); setBody(result) } })
      .catch(requestError => { if (active) setBody({ error: requestError.message }) })
    return () => { active = false }
  }, [api, mailbox.id, selected?.id])

  const html = body?.html || (body?.text ? `<pre style="white-space:pre-wrap;font-family:system-ui">${body.text.replaceAll('&', '&amp;').replaceAll('<', '&lt;')}</pre>` : '')
  return <div className="modal-backdrop" onMouseDown={event => event.target === event.currentTarget && onClose()}>
    <div className="modal mailbox-inbox-modal">
      <div className="mailbox-inbox-head"><div><p className="eyebrow">LIVE INBOX · 3 秒自动刷新</p><h4>{mailbox.email}</h4></div><div><button className="secondary" onClick={fetchMessages}>立即刷新</button><button className="icon-btn" onClick={onClose}>×</button></div></div>
      {error && <div className="alert error">取件失败：{error}</div>}
      <div className="mailbox-inbox-layout">
        <div className="mailbox-message-list">
          {loading && !messages.length && <div className="empty compact">正在读取收件箱…</div>}
          {!loading && !messages.length && !error && <div className="empty compact">暂无邮件，正在等待新邮件…</div>}
          {messages.map(message => <button key={messageKey(message)} className={`mailbox-message-item ${messageKey(message) === messageKey(selected) ? 'active' : ''}`} onClick={() => setSelectedID(messageKey(message))}><span>{message.from_name || message.from || '未知发件人'}</span><strong>{message.subject || '(无主题)'}</strong><small>{formatTime(message.received_at)}</small></button>)}
        </div>
        <article className="mailbox-message-body">
          {!selected && loading && <div className="mailbox-body-loading"><span className="mail-spinner" />正在加载收件箱…</div>}
          {!selected && !loading && <div className="empty">选择一封邮件查看正文</div>}
          {selected && <><div className="mailbox-message-meta"><h4>{selected.subject || '(无主题)'}</h4><p>{selected.from_name || ''} &lt;{selected.from || ''}&gt;</p><small>{formatTime(selected.received_at)}</small></div>{body?.error ? <div className="alert error">{body.error}</div> : html ? <iframe title="邮件正文" sandbox="allow-same-origin allow-popups" srcDoc={html} /> : <div className="mailbox-body-loading"><span className="mail-spinner" />正在加载邮件正文…</div>}</>}
        </article>
      </div>
    </div>
  </div>
}

export default function MailboxPage({ api, notify }) {
  const [rows, setRows] = useState([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [queryInput, setQueryInput] = useState('')
  const [query, setQuery] = useState('')
  const [status, setStatus] = useState('')
  const [selected, setSelected] = useState(new Set())
  const [loading, setLoading] = useState(false)
  const [editor, setEditor] = useState(undefined)
  const [importOpen, setImportOpen] = useState(false)
  const [inbox, setInbox] = useState(null)
  const loadingRef = useRef(false)

  const load = useCallback(async (quiet = false) => {
    if (loadingRef.current) return
    loadingRef.current = true
    if (!quiet) setLoading(true)
    try {
      const params = new URLSearchParams({ page: String(page), size: String(PAGE_SIZE) })
      if (query) params.set('q', query)
      if (status) params.set('status', status)
      const result = await api.get(`/api/mailboxes?${params}`)
      setRows(result.data || [])
      setTotal(result.total || 0)
    } catch (error) { if (!quiet) notify(error.message, 'error') }
    finally { loadingRef.current = false; if (!quiet) setLoading(false) }
  }, [api, notify, page, query, status])

  useEffect(() => {
    load()
    const timer = setInterval(() => { if (!document.hidden) load(true) }, 3000)
    const refresh = () => load()
    window.addEventListener('refresh', refresh)
    return () => { clearInterval(timer); window.removeEventListener('refresh', refresh) }
  }, [load])

  const pages = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const currentIDs = rows.map(row => row.id)
  const allCurrentSelected = currentIDs.length > 0 && currentIDs.every(id => selected.has(id))
  const statusCounts = useMemo(() => rows.reduce((result, row) => ({ ...result, [row.status]: (result[row.status] || 0) + 1 }), {}), [rows])
  const toggle = (id, checked) => setSelected(current => { const next = new Set(current); if (checked) next.add(id); else next.delete(id); return next })
  const togglePage = checked => setSelected(current => { const next = new Set(current); currentIDs.forEach(id => checked ? next.add(id) : next.delete(id)); return next })

  const reauthenticate = async (body, message) => {
    if (!confirm(message)) return
    try {
      const result = await api.post('/api/mailboxes/reauthenticate', body)
      notify(result.queued ? `已提交 ${result.queued} 个邮箱，后台认证中` : '没有符合条件的邮箱')
      setSelected(new Set())
      load()
    } catch (error) { notify(error.message, 'error') }
  }
  const deleteOne = async row => {
    if (!confirm(`确定删除邮箱 ${row.email}？`)) return
    try { await api.delete(`/api/mailboxes/${row.id}`); notify('邮箱已删除'); load() } catch (error) { notify(error.message, 'error') }
  }
  const deleteSelected = async () => {
    const ids = [...selected]
    if (!ids.length || !confirm(`确定删除所选 ${ids.length} 个邮箱？`)) return
    try { await Promise.all(ids.map(id => api.delete(`/api/mailboxes/${id}`))); setSelected(new Set()); notify(`已删除 ${ids.length} 个邮箱`); load() } catch (error) { notify(error.message, 'error') }
  }
  const deleteAll = async () => {
    if (!confirm('确定删除全部邮箱？此操作不可恢复。')) return
    if (!confirm('再次确认：将永久删除邮箱池中的全部记录。')) return
    try { const result = await api.delete('/api/mailboxes'); setSelected(new Set()); setPage(1); notify(`已删除 ${result.deleted || 0} 个邮箱`); load() } catch (error) { notify(error.message, 'error') }
  }
  const search = event => { event.preventDefault(); setPage(1); setQuery(queryInput.trim()) }

  return <div className="stack gap-lg">
    <section className="page-intro"><div><p className="eyebrow">MAILBOX POOL</p><h3>邮箱池</h3><p className="muted">邮箱导入后由后台持久认证队列处理；Grok 注册只使用已验证且未达到注册上限的邮箱。</p></div><div className="toolbar"><button className="secondary" onClick={() => setImportOpen(true)}>批量导入</button><button className="primary" onClick={() => setEditor(null)}>＋ 添加邮箱</button></div></section>
    <section className="panel mailbox-table-panel">
      <div className="mailbox-toolbar">
        <form className="search mailbox-search" onSubmit={search}><span>⌕</span><input placeholder="搜索邮箱、服务商或备注" value={queryInput} onChange={event => setQueryInput(event.target.value)} /></form>
        <select value={status} onChange={event => { setStatus(event.target.value); setPage(1) }}><option value="">全部状态</option>{Object.entries(STATUS_TEXT).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select>
        <button className="secondary" onClick={() => load()} disabled={loading}>{loading ? '刷新中…' : '刷新'}</button>
        <span className="toolbar-spacer" />
        <button className="secondary" onClick={() => reauthenticate({ failed: true }, '确定重新认证全部验证失败的邮箱？')}>认证失败项</button>
        <button className="secondary" onClick={() => reauthenticate({ all: true }, '确定重新认证全部邮箱？认证将在后台继续。')}>认证全部</button>
        <button className="danger-button" onClick={deleteAll} disabled={!total}>全部删除</button>
      </div>
    <div className="mailbox-status-summary"><span>共 <strong>{total}</strong> 个邮箱</span><i /> <span>本页已验证 <strong>{statusCounts.verified || 0}</strong></span><span>验证中 <strong>{statusCounts.verifying || 0}</strong></span><span>失败 <strong>{statusCounts.verify_failed || 0}</strong></span><small>列表每 3 秒自动刷新</small></div>
      {selected.size > 0 && <div className="mailbox-batch-bar"><strong>已选 {selected.size} 项</strong><button className="secondary" onClick={() => reauthenticate({ ids: [...selected] }, `确定重新认证所选 ${selected.size} 个邮箱？`)}>重新认证所选</button><button className="danger-button" onClick={deleteSelected}>删除所选</button><button className="link-btn" onClick={() => setSelected(new Set())}>取消选择</button></div>}
      <div className="table-scroll"><table className="mailbox-table"><thead><tr><th className="check-cell"><input type="checkbox" checked={allCurrentSelected} onChange={event => togglePage(event.target.checked)} /></th><th>邮箱</th><th>注册用量</th><th>创建时间</th><th>认证状态</th><th></th></tr></thead><tbody>{rows.map(row => <tr key={row.id} className={selected.has(row.id) ? 'selected-row' : ''}><td className="check-cell"><input type="checkbox" checked={selected.has(row.id)} onChange={event => toggle(row.id, event.target.checked)} /></td><td><strong>{row.email}</strong><small>{[row.provider, row.note].filter(Boolean).join(' · ') || '未填写备注'}</small></td><td><div className="usage-cell"><strong>{row.register_count || 0} / {row.register_limit || 0}</strong><span><i style={{ width: `${Math.min(100, ((row.register_count || 0) / Math.max(1, row.register_limit || 1)) * 100)}%` }} /></span></div></td><td>{formatTime(row.created_at)}</td><td><span className={`badge ${row.status === 'verified' ? 'success' : row.status === 'verify_failed' ? 'danger' : 'warning'}`}>{STATUS_TEXT[row.status] || row.status}</span>{row.verify_error && <small className="verify-error" title={row.verify_error}>{row.verify_error}</small>}</td><td><div className="row-actions mailbox-row-actions">{row.status === 'verified' && <button className="mailbox-icon-btn" title="查看收件箱" aria-label={`查看 ${row.email} 收件箱`} onClick={() => setInbox(row)}><svg viewBox="0 0 24 24" aria-hidden="true"><rect x="3" y="5" width="18" height="14" rx="2" /><path d="m3 7 9 6 9-6" /></svg></button>}<button onClick={() => setEditor(row)}>编辑</button><button onClick={() => reauthenticate({ ids: [row.id] }, `确定重新认证 ${row.email}？`)}>认证</button><button className="danger-text" onClick={() => deleteOne(row)}>删除</button></div></td></tr>)}</tbody></table>{!rows.length && <div className="empty">{loading ? '正在加载邮箱…' : '没有符合条件的邮箱，请先批量导入'}</div>}</div>
      <div className="pagination"><span>第 {page} 页 · 共 {total} 条</span><div><button disabled={page <= 1} onClick={() => setPage(current => current - 1)}>上一页</button><b>{page} / {pages}</b><button disabled={page >= pages} onClick={() => setPage(current => current + 1)}>下一页</button></div></div>
    </section>
    {importOpen && <ImportModal api={api} notify={notify} onClose={() => setImportOpen(false)} onImported={() => { setPage(1); load() }} />}
    {editor !== undefined && <EditorModal api={api} mailbox={editor} notify={notify} onClose={() => setEditor(undefined)} onSaved={load} />}
    {inbox && <InboxModal api={api} mailbox={inbox} onClose={() => setInbox(null)} />}
  </div>
}
