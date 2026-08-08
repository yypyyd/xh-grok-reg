import React, { useEffect, useState } from 'react'
import { createRoot } from 'react-dom/client'
import './styles.css'
import './proxy.css'
import './layout-fixes.css'
import './mailbox.css'
import MailboxPage from './MailboxPage.jsx'
import DashboardPage from './DashboardPage.jsx'
import GrokPageV2 from './GrokPage.jsx'
import SettingsPage from './SettingsPage.jsx'

const api = {
  token: localStorage.getItem('grok_token') || '',
  async request(path, options = {}) {
    const headers = { 'Content-Type': 'application/json', ...(options.headers || {}) }
    if (this.token) headers.Authorization = `Bearer ${this.token}`
    const res = await fetch(path, { ...options, headers })
    const newToken = res.headers.get('X-New-Token')
    if (newToken) { this.token = newToken; localStorage.setItem('grok_token', newToken) }
    let body = null
    try { body = await res.json() } catch { /* binary/empty */ }
    if (res.status === 401) { this.token = ''; localStorage.removeItem('grok_token'); window.dispatchEvent(new Event('logout')) }
    if (!res.ok) throw new Error(body?.error || `请求失败（${res.status}）`)
    return body
  },
  get(path) { return this.request(path) },
  post(path, body) { return this.request(path, { method: 'POST', body: body === undefined ? undefined : JSON.stringify(body) }) },
  put(path, body) { return this.request(path, { method: 'PUT', body: JSON.stringify(body) }) },
  delete(path) { return this.request(path, { method: 'DELETE' }) }
}

function Login({ onLogin }) {
  const [username, setUsername] = useState('admin')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const submit = async (e) => { e.preventDefault(); setError(''); try { const r = await api.post('/api/login', { username, password }); api.token = r.token; localStorage.setItem('grok_token', r.token); onLogin(r.username) } catch (err) { setError(err.message) } }
  return <main className="login-page"><div className="login-card"><div className="brand-mark">G</div><p className="eyebrow">XH / CONTROL ROOM</p><h1>Grok 注册工作台</h1><p className="muted">把邮箱池、注册任务和账号库存放在一个清晰的控制面板里。</p><form onSubmit={submit} className="stack"><label>管理员账号<input value={username} onChange={e => setUsername(e.target.value)} autoComplete="username" /></label><label>密码<input type="password" value={password} onChange={e => setPassword(e.target.value)} autoComplete="current-password" /></label>{error && <div className="alert error">{error}</div>}<button className="primary wide">进入控制台</button></form><p className="hint">首次运行默认账号：admin / admin123</p></div></main>
}

function Shell({ user, page, setPage, onLogout, children }) {
  const nav = [['dashboard', '总览', '⌂'], ['grok', 'Grok 账号', '◈'], ['mailboxes', '邮箱池', '✉'], ['settings', '系统设置', '⚙']]
  return <div className="app-shell"><aside className="sidebar"><div className="logo"><span className="brand-mark small">G</span><div><strong>XH Grok</strong><small>control room</small></div></div><nav>{nav.map(([id, label, icon]) => <button key={id} className={page === id ? 'active' : ''} onClick={() => setPage(id)}><span>{icon}</span>{label}</button>)}</nav><div className="sidebar-foot"><div className="user-chip"><span className="avatar">{(user || 'A')[0].toUpperCase()}</span><div><strong>{user}</strong><small>管理员</small></div></div><button className="ghost" onClick={onLogout}>退出登录</button></div></aside><main className="main"><header className="topbar"><div><p className="eyebrow">WORKSPACE / {page.toUpperCase()}</p><h2>{nav.find(n => n[0] === page)?.[1] || '总览'}</h2></div><div className="top-actions"><span className="status-dot"><i />服务在线</span><button className="icon-btn" title="刷新" onClick={() => window.dispatchEvent(new Event('refresh'))}>↻</button></div></header><div className="content">{children}</div></main></div>
}

function StatCard({ label, value, tone = 'blue', detail }) { return <div className="stat-card"><div className={`stat-icon ${tone}`}>{tone === 'green' ? '✓' : tone === 'orange' ? '◷' : tone === 'red' ? '!' : '◈'}</div><div><span>{label}</span><strong>{value ?? '—'}</strong>{detail && <small>{detail}</small>}</div></div> }

function Dashboard({ notify }) {
  const [stats, setStats] = useState(null); const [browser, setBrowser] = useState(null); const [error, setError] = useState('')
  const load = async () => { try { const [s, b] = await Promise.all([api.get('/api/stats'), api.get('/api/browser/status')]); setStats(s); setBrowser(b); setError('') } catch (e) { setError(e.message) } }
  useEffect(() => { load(); const id = setInterval(load, 5000); const f = () => load(); window.addEventListener('refresh', f); return () => { clearInterval(id); window.removeEventListener('refresh', f) } }, [])
  const progress = stats?.produce_target ? Math.min(100, Math.round(((stats.produce_registered + stats.produce_failed) / stats.produce_target) * 100)) : 0
  return <div className="stack gap-lg">{error && <div className="alert error">{error}</div>}<div className="hero"><div><p className="eyebrow">GOOD MORNING</p><h3>今天也让注册流程保持顺畅。</h3><p className="muted">监控 Grok 账号库存和邮箱验证状态，所有关键动作都在这里完成。</p></div><div className="hero-orb"><span>{stats?.registered ?? 0}</span><small>已注册账号</small></div></div><div className="stat-grid"><StatCard label="账号总量" value={stats?.total} detail={`${stats?.unshipped ?? 0} 个待出库`} /><StatCard label="已注册" value={stats?.registered} tone="green" detail={`${stats?.shipped ?? 0} 个已出库`} /><StatCard label="进行中" value={stats?.registering} tone="orange" detail={stats?.produce_message || '暂无批量任务'} /><StatCard label="失败记录" value={stats?.register_failed} tone="red" detail="可重新发起注册" /></div><div className="two-col"><section className="panel"><div className="panel-head"><div><p className="eyebrow">PRODUCTION</p><h4>生产任务</h4></div><span className={`badge ${stats?.running ? 'warning' : 'neutral'}`}>{stats?.running ? '运行中' : '空闲'}</span></div><div className="progress-wrap"><div className="progress-label"><span>{stats?.produce_message || '等待开始新的批量任务'}</span><strong>{stats?.produce_target ? `${progress}%` : '—'}</strong></div><div className="progress"><i style={{ width: `${progress}%` }} /></div><div className="progress-meta"><span>已完成 {stats?.produce_registered ?? 0}</span><span>失败 {stats?.produce_failed ?? 0}</span><span>目标 {stats?.produce_target ?? 0}</span></div></div></section><section className="panel"><div className="panel-head"><div><p className="eyebrow">MAILBOX HEALTH</p><h4>邮箱池状态</h4></div><button className="link-btn" onClick={() => notify('请从左侧进入邮箱池管理')}>管理邮箱 →</button></div><div className="mail-health"><div className="ring" style={{ '--value': `${stats?.mailboxes ? Math.round((stats.mailbox_verified / stats.mailboxes) * 100) : 0}%` }}><span>{stats?.mailboxes ? Math.round((stats.mailbox_verified / stats.mailboxes) * 100) : 0}%</span></div><div><strong>{stats?.mailbox_verified ?? 0} / {stats?.mailboxes ?? 0}</strong><p className="muted">邮箱凭据已验证</p><small className="muted">验证邮箱越多，批量生产越稳定。</small></div></div></section></div><section className="panel browser-panel"><div><p className="eyebrow">BROWSER RUNTIME</p><h4>Chromium 运行环境</h4><p className="muted">{browser?.message || '正在检查浏览器状态...'}</p></div><span className={`badge ${browser?.ready ? 'success' : browser?.phase === 'error' ? 'danger' : 'warning'}`}>{browser?.ready ? 'READY' : (browser?.phase || 'CHECKING').toUpperCase()}</span></section></div>
}

function GrokPage({ notify }) {
  const [rows, setRows] = useState([]); const [total, setTotal] = useState(0); const [query, setQuery] = useState(''); const [status, setStatus] = useState(''); const [page, setPage] = useState(1); const [busy, setBusy] = useState(false); const [count, setCount] = useState(1); const [codeId, setCodeId] = useState(null); const [code, setCode] = useState(''); const [detail, setDetail] = useState(null);
  const load = async () => { try { const r = await api.get(`/api/grok/registrations?page=${page}&size=12&q=${encodeURIComponent(query)}&status=${status}`); setRows(r.data || []); setTotal(r.total || 0) } catch (e) { notify(e.message, 'error') } }
  useEffect(() => { load(); const f = () => load(); window.addEventListener('refresh', f); return () => window.removeEventListener('refresh', f) }, [page, status])
  const start = async () => { setBusy(true); try { await api.post('/api/grok/produce', { count: Number(count) }); notify('批量注册任务已启动'); load() } catch (e) { notify(e.message, 'error') } finally { setBusy(false) } }
  const submitCode = async () => { try { await api.post(`/api/grok/registrations/${codeId}/code`, { code }); notify('验证码已提交'); setCodeId(null); setCode(''); load() } catch (e) { notify(e.message, 'error') } }
  const live = async (id) => { try { const r = await api.post(`/api/grok/registrations/${id}/livecheck`); notify(`${r.alive || 'unknown'} · ${r.console_quota || '无额度信息'}`) } catch (e) { notify(e.message, 'error') } }
  const showLog = async (id) => { try { const r = await api.get(`/api/grok/registrations/${id}/logs`); setDetail({ title: `任务 #${id} 日志`, text: r.log || '暂无日志' }) } catch (e) { notify(e.message, 'error') } }
  const showShot = async (id) => { try { const r = await fetch(`/api/grok/registrations/${id}/shot`, { headers: { Authorization: `Bearer ${api.token}` } }); if (!r.ok) throw new Error('暂无截图'); const url = URL.createObjectURL(await r.blob()); setDetail({ title: `任务 #${id} 截图`, image: url }) } catch (e) { notify(e.message, 'error') } }
  const download = async (format) => { try { const r = await fetch('/api/grok/download', { method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${api.token}` }, body: JSON.stringify({ format, unshipped_only: true }) }); if (!r.ok) throw new Error('导出失败'); const blob = await r.blob(); const a = document.createElement('a'); a.href = URL.createObjectURL(blob); a.download = `grok-${format}.json`; a.click(); notify('导出已开始') } catch (e) { notify(e.message, 'error') } }
  const pages = Math.max(1, Math.ceil(total / 12));
  return <div className="stack gap-lg"><section className="page-intro"><div><p className="eyebrow">ACCOUNT INVENTORY</p><h3>Grok 账号</h3><p className="muted">管理注册任务、会话凭据与账号存活状态。</p></div><div className="toolbar"><button className="secondary" onClick={() => download('console')}>导出 Console</button><button className="secondary" onClick={() => download('sub2api')}>导出 Sub2API</button><label className="produce-count">注册数量<input className="count-input" type="number" min="1" value={count} onChange={e => setCount(e.target.value)} /></label><button className="primary" onClick={start} disabled={busy}>＋ 批量注册</button></div></section><GrokDiagnostics notify={notify} /><section className="panel"><div className="table-tools"><div className="search"><span>⌕</span><input placeholder="搜索邮箱或备注" value={query} onChange={e => setQuery(e.target.value)} onKeyDown={e => e.key === 'Enter' && (setPage(1), load())} /></div><div className="table-filters"><select value={status} onChange={e => { setStatus(e.target.value); setPage(1) }}><option value="">全部状态</option><option value="registered">已注册</option><option value="registering">注册中</option><option value="waiting_code">等待验证码</option><option value="register_failed">失败</option></select><button className="icon-btn" onClick={load}>↻</button></div></div><div className="table-scroll"><table><thead><tr><th>账号</th><th>状态</th><th>存活</th><th>额度</th><th>创建时间</th><th></th></tr></thead><tbody>{rows.map(r => <tr key={r.id}><td><strong>{r.email}</strong><small>{r.note || '—'}</small></td><td><span className={`badge ${r.status === 'registered' ? 'success' : r.status === 'register_failed' ? 'danger' : 'warning'}`}>{({ registered: '已注册', registering: '注册中', waiting_code: '待验证码', register_failed: '失败', pending: '待处理' })[r.status] || r.status}</span></td><td><span className={`alive ${r.alive || 'unknown'}`}><i />{r.alive || '未测'}</span></td><td>{r.console_quota || '—'}</td><td>{r.created_at ? new Date(r.created_at).toLocaleString('zh-CN') : '—'}</td><td><div className="row-actions"><button onClick={() => live(r.id)}>测活</button>{r.status === 'waiting_code' && <button onClick={() => setCodeId(r.id)}>填码</button>}<button className="danger-text" onClick={async () => { if (confirm('确认删除此账号？')) { await api.delete(`/api/grok/registrations/${r.id}`); load() } }}>删除</button></div></td></tr>)}</tbody></table>{!rows.length && <div className="empty">暂无匹配账号</div>}</div><div className="pagination"><span>共 {total} 条</span><div><button disabled={page <= 1} onClick={() => setPage(page - 1)}>上一页</button><b>{page} / {pages}</b><button disabled={page >= pages} onClick={() => setPage(page + 1)}>下一页</button></div></div></section>{codeId && <div className="modal-backdrop"><div className="modal"><h4>提交邮箱验证码</h4><p className="muted">任务 #{codeId} 正在等待验证码。</p><input autoFocus value={code} onChange={e => setCode(e.target.value)} placeholder="输入 6 位验证码" /><div className="modal-actions"><button className="secondary" onClick={() => setCodeId(null)}>取消</button><button className="primary" onClick={submitCode}>提交</button></div></div></div>}</div>
}

function Settings({ notify }) { const [values, setValues] = useState({}); useEffect(() => { api.get('/api/settings').then(setValues).catch(e => notify(e.message, 'error')) }, []); const save = async (e) => { e.preventDefault(); try { await api.put('/api/settings', values); notify('设置已保存') } catch (e) { notify(e.message, 'error') } }; return <div className="stack gap-lg"><section className="page-intro"><div><p className="eyebrow">WORKSPACE CONFIG</p><h3>系统设置</h3><p className="muted">调整并发、代理和浏览器运行策略。</p></div></section><form className="panel settings-form" onSubmit={save}><div className="form-grid"><label>最大并发<input type="number" min="1" value={values.max_concurrency || ''} onChange={e => setValues({ ...values, max_concurrency: e.target.value })} placeholder="1" /></label><label>无头模式<select value={values.grok_headless || '1'} onChange={e => setValues({ ...values, grok_headless: e.target.value })}><option value="1">开启</option><option value="0">关闭</option></select></label><label>启用代理<select value={values.proxy_enabled || '0'} onChange={e => setValues({ ...values, proxy_enabled: e.target.value })}><option value="1">开启</option><option value="0">关闭</option></select></label><label className="full">代理列表<textarea rows="5" value={values.proxy_list || ''} onChange={e => setValues({ ...values, proxy_list: e.target.value })} placeholder="每行一个代理地址" /></label></div><div className="form-footer"><span className="muted">设置会写入本地 SQLite 数据库。</span><button className="primary">保存设置</button></div></form></div> }

function ProxySettings({ notify }) {
  const [proxy, setProxy] = useState('')
  const [testing, setTesting] = useState(false)
  const [result, setResult] = useState(null)
  const test = async () => {
    if (!proxy.trim()) { notify('请输入代理地址', 'error'); return }
    setTesting(true); setResult(null)
    try {
      const data = await api.post('/api/proxy/test', { proxy: proxy.trim() })
      setResult(data.ok ? { ok: true, text: `代理可用 · 出口 IP ${data.ip || '未知'} · ${data.ms || 0} ms` } : { ok: false, text: data.error || '代理不可用' })
    } catch (e) { setResult({ ok: false, text: e.message }) }
    finally { setTesting(false) }
  }
  return <section className="panel proxy-test-panel"><div className="panel-head"><div><p className="eyebrow">PROXY DIAGNOSTICS</p><h4>代理连通性测试</h4><p className="muted">先测试单条代理，再保存到代理列表。支持 http、https、socks5 和 host:port:user:pass 格式。</p></div><span className="proxy-symbol">↗</span></div><div className="proxy-test-row"><input value={proxy} onChange={e => setProxy(e.target.value)} onKeyDown={e => e.key === 'Enter' && test()} placeholder="http://user:pass@host:port" /><button type="button" className="secondary" onClick={test} disabled={testing}>{testing ? '测试中…' : '测试代理'}</button></div>{result && <div className={`proxy-result ${result.ok ? 'ok' : 'bad'}`}><i />{result.text}</div>}</section>
}

function GrokDiagnostics({ notify }) {
  const [accounts, setAccounts] = useState([])
  const [selected, setSelected] = useState('')
  const [detail, setDetail] = useState(null)
  const load = async () => { try { const result = await api.get('/api/grok/registrations?page=1&size=100'); const data = result.data || []; setAccounts(data); if (!selected && data[0]) setSelected(String(data[0].id)) } catch (e) { notify(e.message, 'error') } }
  useEffect(() => { load(); const refresh = () => load(); window.addEventListener('refresh', refresh); return () => window.removeEventListener('refresh', refresh) }, [])
  const showLog = async () => { if (!selected) return; try { const result = await api.get(`/api/grok/registrations/${selected}/logs`); setDetail({ title: `Grok #${selected} 执行日志`, text: result.log || '暂无日志' }) } catch (e) { notify(e.message, 'error') } }
  const showShot = async () => { if (!selected) return; try { const response = await fetch(`/api/grok/registrations/${selected}/shot`, { headers: { Authorization: `Bearer ${api.token}` } }); if (!response.ok) throw new Error('该账号暂无截图'); setDetail({ title: `Grok #${selected} 失败截图`, image: URL.createObjectURL(await response.blob()) }) } catch (e) { notify(e.message, 'error') } }
  return <><section className="diagnostics-strip"><span className="eyebrow">ACCOUNT TOOLS</span><select value={selected} onChange={e => setSelected(e.target.value)}><option value="">{accounts.length ? '选择 Grok 账号' : '暂无 Grok 账号'}</option>{accounts.map(account => <option key={account.id} value={account.id}>{account.email}</option>)}</select><button className="secondary" onClick={showLog} disabled={!selected}>查看日志</button><button className="secondary" onClick={showShot} disabled={!selected}>查看截图</button></section>{detail && <div className="modal-backdrop"><div className="modal wide-modal"><h4>{detail.title}</h4>{detail.image ? <img className="shot" src={detail.image} alt="注册截图" /> : <pre className="log-view">{detail.text}</pre>}<div className="modal-actions"><button className="secondary" onClick={() => { if (detail.image) URL.revokeObjectURL(detail.image); setDetail(null) }}>关闭</button></div></div></div>}</>
}

function App() {
  const [user, setUser] = useState(localStorage.getItem('grok_user') || '')
  const [page, setPage] = useState('dashboard'); const [toast, setToast] = useState(null)
  useEffect(() => { const f = () => setUser(''); window.addEventListener('logout', f); return () => window.removeEventListener('logout', f) }, [])
  const notify = (message, kind = 'success') => { setToast({ message, kind }); setTimeout(() => setToast(null), 3600) }
  if (!user || !api.token) return <Login onLogin={u => { setUser(u); localStorage.setItem('grok_user', u) }} />
  const logout = () => { api.token = ''; localStorage.removeItem('grok_token'); localStorage.removeItem('grok_user'); setUser('') }
  return <><Shell user={user} page={page} setPage={setPage} onLogout={logout}>{page === 'dashboard' && <DashboardPage api={api} notify={notify} setPage={setPage} />}{page === 'grok' && <GrokPageV2 api={api} notify={notify} />}{page === 'mailboxes' && <MailboxPage api={api} notify={notify} />}{page === 'settings' && <SettingsPage api={api} notify={notify} />}</Shell>{toast && <div className={`toast ${toast.kind === 'error' ? 'error' : ''}`}>{toast.message}</div>}</>
}

createRoot(document.getElementById('root')).render(<App />)
