import React, { useCallback, useEffect, useMemo, useState } from 'react'
import './dashboard.css'

const STATUS = {
  registered: ['已注册', 'success', '#2db477'],
  registering: ['注册中', 'warning', '#dd9a37'],
  waiting_code: ['待验证码', 'warning', '#e09b36'],
  pending: ['待处理', 'neutral', '#8c9aaa'],
  register_failed: ['失败', 'danger', '#d85966'],
}

function Donut({ stats }) {
  const parts = useMemo(() => [
    ['registered', Number(stats?.registered || 0)],
    ['registering', Number(stats?.registering || 0)],
    ['pending', Number(stats?.pending || 0)],
    ['register_failed', Number(stats?.register_failed || 0)],
  ].filter(([, value]) => value > 0), [stats])
  const total = parts.reduce((sum, [, value]) => sum + value, 0)
  let cursor = 0
  const stops = parts.map(([key, value]) => {
    const from = cursor / Math.max(total, 1) * 100
    cursor += value
    return `${STATUS[key][2]} ${from}% ${cursor / Math.max(total, 1) * 100}%`
  })
  return <div className="dashboard-donut-wrap"><div className="dashboard-donut" style={{ background: total ? `conic-gradient(${stops.join(',')})` : 'conic-gradient(#edf1f6 0 100%)' }}><div><strong>{total}</strong><span>账号</span></div></div><div className="dashboard-legend">{parts.length ? parts.map(([key, value]) => <div key={key}><i style={{ background: STATUS[key][2] }} /><span>{STATUS[key][0]}</span><b>{value}</b><em>{Math.round(value / total * 100)}%</em></div>) : <span className="dashboard-empty">暂无账号</span>}</div></div>
}

export default function DashboardPage({ api, notify, setPage }) {
  const [stats, setStats] = useState(null)
  const [browser, setBrowser] = useState(null)
  const [produce, setProduce] = useState(null)
  const [recent, setRecent] = useState([])
  const [error, setError] = useState('')
  const load = useCallback(async () => {
    try {
      const [s, b, p, r] = await Promise.all([
        api.get('/api/stats'), api.get('/api/browser/status'), api.get('/api/grok/produce/status'), api.get('/api/grok/registrations?page=1&size=8'),
      ])
      setStats(s); setBrowser(b); setProduce(p); setRecent(r?.data || []); setError('')
    } catch (e) { setError(e.message) }
  }, [api])
  useEffect(() => { load(); const timer = setInterval(load, 4000); const refresh = () => load(); window.addEventListener('refresh', refresh); return () => { clearInterval(timer); window.removeEventListener('refresh', refresh) } }, [load])

  const target = Number(produce?.target || stats?.produce_target || 0)
  const done = Number(produce?.registered || stats?.produce_registered || 0)
  const progress = target ? Math.min(100, Math.round(done / target * 100)) : 0
  const browserNeedsAttention = browser && !browser.ready
  return <div className="dashboard-stack">
    {error && <div className="alert error">{error}</div>}
    {browserNeedsAttention && <section className={`browser-banner ${browser.phase === 'error' ? 'failed' : ''}`}><div className="browser-banner-icon">{browser.phase === 'error' ? '!' : '↓'}</div><div className="browser-banner-copy"><strong>{browser.message || '浏览器尚未就绪'}</strong><span>{browser.error || '浏览器就绪后才能开始 Grok 注册任务。'}</span></div><div className="browser-banner-state">{browser.phase === 'downloading' || browser.phase === 'unzip' ? `${browser.percent || 0}%` : browser.phase === 'error' ? '需处理' : '检查中'}</div>{(browser.phase === 'downloading' || browser.phase === 'unzip') && <div className="browser-banner-progress"><i style={{ width: `${browser.percent || 0}%` }} /></div>}</section>}
    <section className="dashboard-hero"><div><p className="eyebrow">GROK / OPERATIONS</p><h3>注册工作流，一眼掌握。</h3><p className="muted">从邮箱验证到账号出库，实时查看每一步的库存和任务状态。</p><div className="dashboard-hero-actions"><button className="primary" onClick={() => setPage?.('grok')}>管理 Grok 账号</button><button className="secondary" onClick={() => setPage?.('mailboxes')}>检查邮箱池</button></div></div><div className="hero-orb"><span>{stats?.registered ?? 0}</span><small>已注册账号</small></div></section>
    <div className="stat-grid dashboard-stat-grid"><div className="stat-card"><div className="stat-icon blue">◈</div><div><span>账号总量</span><strong>{stats?.total ?? '—'}</strong><small>{stats?.unshipped ?? 0} 个待出库</small></div></div><div className="stat-card"><div className="stat-icon green">✓</div><div><span>已注册</span><strong>{stats?.registered ?? '—'}</strong><small>{stats?.shipped ?? 0} 个已出库</small></div></div><div className="stat-card"><div className="stat-icon orange">◷</div><div><span>进行中</span><strong>{stats?.registering ?? '—'}</strong><small>{produce?.running ? '批量任务运行中' : '暂无批量任务'}</small></div></div><div className="stat-card"><div className="stat-icon red">!</div><div><span>失败记录</span><strong>{stats?.register_failed ?? '—'}</strong><small>可重新发起注册</small></div></div><div className="stat-card violet"><div className="stat-icon purple">⇪</div><div><span>可出库库存</span><strong>{stats?.unshipped ?? '—'}</strong><small>已注册未导出</small></div></div><div className="stat-card cyan"><div className="stat-icon teal">✉</div><div><span>可用邮箱</span><strong>{stats?.mailbox_verified ?? '—'}</strong><small>共 {stats?.mailboxes ?? 0} 个邮箱</small></div></div></div>
    <section className="panel dashboard-production"><div className="panel-head"><div><p className="eyebrow">PRODUCTION QUEUE</p><h4>生产任务</h4></div><span className={`badge ${produce?.running ? 'warning' : 'neutral'}`}>{produce?.running ? '运行中' : '空闲'}</span></div><div className="production-main"><div className="production-count"><strong>{done}</strong><span>/ {target || 0}</span><small>已注册 / 目标</small></div><div className="production-progress"><div className="progress-label"><span>{produce?.message || stats?.produce_message || '等待开始新的批量任务'}</span><b>{target ? `${progress}%` : '—'}</b></div><div className="progress"><i style={{ width: `${progress}%` }} /></div></div><div className="production-pills"><div><b>{produce?.pending ?? stats?.produce_pending ?? 0}</b><span>待生产</span></div><div><b>{produce?.running_num ?? stats?.produce_running ?? 0}</b><span>在跑</span></div><div><b>{produce?.failed ?? stats?.produce_failed ?? 0}</b><span>失败</span></div></div></div></section>
    <div className="dashboard-grid"><section className="panel"><div className="panel-head"><div><p className="eyebrow">ACCOUNT MIX</p><h4>账号状态占比</h4></div></div><Donut stats={stats} /></section><section className="panel"><div className="panel-head"><div><p className="eyebrow">MAILBOX HEALTH</p><h4>邮箱池健康度</h4></div><button className="link-btn" onClick={() => setPage?.('mailboxes')}>管理邮箱 →</button></div><div className="dashboard-mail-health"><div className="ring" style={{ '--value': `${stats?.mailboxes ? Math.round(stats.mailbox_verified / stats.mailboxes * 100) : 0}%` }}><span>{stats?.mailboxes ? Math.round(stats.mailbox_verified / stats.mailboxes * 100) : 0}%</span></div><div><strong>{stats?.mailbox_verified ?? 0} / {stats?.mailboxes ?? 0}</strong><p className="muted">邮箱凭据已验证</p><small className="muted">验证邮箱越多，批量生产越稳定。</small></div></div></section></div>
    <section className="panel dashboard-recent"><div className="panel-head"><div><p className="eyebrow">LATEST ACCOUNTS</p><h4>最近 Grok 账号</h4></div><button className="link-btn" onClick={() => setPage?.('grok')}>查看全部 →</button></div><div className="table-scroll"><table><thead><tr><th>ID</th><th>邮箱</th><th>状态</th><th>出库</th><th>创建时间</th></tr></thead><tbody>{recent.map(row => <tr key={row.id}><td>#{row.id}</td><td><strong>{row.email}</strong><small>{row.note || '—'}</small></td><td><span className={`badge ${STATUS[row.status]?.[1] || 'neutral'}`}>{STATUS[row.status]?.[0] || row.status}</span></td><td>{row.shipped ? <span className="shipped-label">已出库</span> : <span className="muted">未出库</span>}</td><td>{row.created_at ? new Date(row.created_at).toLocaleString('zh-CN') : '—'}</td></tr>)}{!recent.length && <tr><td colSpan="5"><div className="empty">暂无账号记录</div></td></tr>}</tbody></table></div></section>
    <section className="panel browser-panel"><div><p className="eyebrow">BROWSER RUNTIME</p><h4>Chromium 运行环境</h4><p className="muted">{browser?.message || '正在检查浏览器状态...'}</p></div><span className={`badge ${browser?.ready ? 'success' : browser?.phase === 'error' ? 'danger' : 'warning'}`}>{browser?.ready ? 'READY' : (browser?.phase || 'CHECKING').toUpperCase()}</span></section>
  </div>
}
