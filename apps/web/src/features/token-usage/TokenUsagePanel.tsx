import { useEffect, useMemo, useState } from "react";
import { Bot, CircleDollarSign, Cpu, Gauge, Layers3, Zap } from "lucide-react";

import { cypApi } from "../../shared/api/client";
import type { TokenUsageDimension, TokenUsageTrend } from "../../shared/api/types";
import { usePollingResource } from "../../shared/hooks/usePollingResource";
import { EmptyState } from "../../shared/ui/EmptyState";
import { Panel } from "../../shared/ui/Panel";

const ranges = [1, 7, 30, 90] as const;

function formatTokens(value: number): string {
	return new Intl.NumberFormat("zh-CN", { notation: "compact", maximumFractionDigits: 1 }).format(value);
}

function formatUSD(value: number): string {
	return `$${value.toFixed(value < 1 ? 4 : 2)}`;
}

function statusLabel(status: string): string {
	if (status === "success") return "成功";
	if (status === "budget_rejected") return "预算拦截";
	return "失败";
}

function UsageChart({ points, bucket }: { points: TokenUsageTrend[]; bucket: "hour" | "day" }) {
	const geometry = useMemo(() => {
		const width = 920;
		const height = 180;
		const left = 44;
		const right = 16;
		const top = 18;
		const bottom = 28;
		const max = Math.max(1, ...points.map((point) => point.total_tokens));
		const x = (index: number) => left + (index / Math.max(points.length - 1, 1)) * (width - left - right);
		const y = (value: number) => top + (1 - value / max) * (height - top - bottom);
		return {
			width,
			height,
			max,
			line: points.map((point, index) => `${x(index)},${y(point.total_tokens)}`).join(" "),
			area: points.length
				? `${left},${height - bottom} ${points.map((point, index) => `${x(index)},${y(point.total_tokens)}`).join(" ")} ${width - right},${height - bottom}`
				: "",
			x,
			y,
		};
	}, [points]);

	if (!points.length) return <EmptyState>暂无模型调用，产生调用后会显示趋势。</EmptyState>;
	const labelStep = Math.max(1, Math.ceil(points.length / 6));
	return (
		<div className="token-chart" aria-label="Token 使用趋势">
			<svg viewBox={`0 0 ${geometry.width} ${geometry.height}`} role="img">
				<defs>
					<linearGradient id="token-area" x1="0" y1="0" x2="0" y2="1">
						<stop offset="0" stopColor="var(--accent)" stopOpacity="0.28" />
						<stop offset="1" stopColor="var(--accent)" stopOpacity="0" />
					</linearGradient>
				</defs>
				{[0, 0.5, 1].map((ratio) => (
					<g key={ratio}>
						<line className="token-chart__grid" x1="44" x2="904" y1={geometry.y(geometry.max * ratio)} y2={geometry.y(geometry.max * ratio)} />
						<text className="token-chart__axis" x="38" y={geometry.y(geometry.max * ratio) + 3} textAnchor="end">{formatTokens(geometry.max * ratio)}</text>
					</g>
				))}
				<polygon points={geometry.area} fill="url(#token-area)" />
				<polyline className="token-chart__line" points={geometry.line} />
				{points.map((point, index) => index % labelStep === 0 || index === points.length - 1 ? (
					<text key={point.start} className="token-chart__axis" x={geometry.x(index)} y="172" textAnchor="middle">
						{new Date(point.start).toLocaleString("zh-CN", bucket === "day"
							? { month: "numeric", day: "numeric" }
							: { month: "numeric", day: "numeric", hour: "2-digit" })}
					</text>
				) : null)}
			</svg>
		</div>
	);
}

function DimensionList({ title, items }: { title: string; items: TokenUsageDimension[] }) {
	const max = Math.max(1, ...items.map((item) => item.total_tokens));
	return (
		<div className="token-dimension">
			<span className="token-dimension__title">{title}</span>
			{items.length ? items.slice(0, 5).map((item) => (
				<div className="token-dimension__row" key={item.key}>
					<div><strong title={item.key}>{item.key}</strong><small>{item.calls} 次 · {formatUSD(item.cost_usd)}</small></div>
					<span><i style={{ width: `${Math.max(3, item.total_tokens / max * 100)}%` }} /></span>
					<b>{formatTokens(item.total_tokens)}</b>
				</div>
			)) : <small className="token-dimension__empty">暂无数据</small>}
		</div>
	);
}

export function TokenUsagePanel() {
	const [days, setDays] = useState<(typeof ranges)[number]>(7);
	const usage = usePollingResource(() => cypApi.tokenUsage(days), 30_000);

	useEffect(() => {
		void usage.refresh();
	}, [days, usage.refresh]);

	const report = usage.data;
	const today = report?.today;
	const utilization = Math.min(100, Math.max(0, (today?.utilization ?? 0) * 100));
	const tone = today?.paused ? "bad" : today?.level === "critical" || today?.level === "warning" ? "warn" : "ok";

	return (
		<Panel
			title="模型调用与 Token 成本"
			icon={<Cpu />}
			className="token-usage-panel"
			actions={(
				<div className="market-range" aria-label="统计范围">
					{ranges.map((range) => <button key={range} type="button" className={days === range ? "is-active" : ""} onClick={() => setDays(range)}>{range}天</button>)}
				</div>
			)}
		>
			{usage.error ? <div className="inline-alert">Token 统计读取失败：{usage.error}</div> : null}
			<div className="token-summary-grid">
				<article><Zap /><span>今日 Token</span><strong>{formatTokens(today?.total_tokens ?? 0)}</strong><small>输入 {formatTokens(today?.input_tokens ?? 0)} · 输出 {formatTokens(today?.output_tokens ?? 0)}</small></article>
				<article><CircleDollarSign /><span>今日成本</span><strong>{formatUSD(today?.cost_usd ?? 0)}</strong><small>日预算 {formatUSD(today?.cost_budget_usd ?? 0)}</small></article>
				<article><Bot /><span>调用次数</span><strong>{today?.calls ?? 0}</strong><small>失败 {today?.errors ?? 0} · 拦截 {today?.budget_rejections ?? 0}</small></article>
				<article><Gauge /><span>成功率</span><strong>{today?.calls ? `${((today.success_rate ?? 0) * 100).toFixed(1)}%` : "--"}</strong><small>{today?.timezone ?? "Asia/Shanghai"} 自然日</small></article>
			</div>

			<div className={`token-budget token-budget--${tone}`}>
				<div><span>{today?.paused ? "今日模型分析已暂停" : "每日模型预算"}</span><b>{utilization.toFixed(1)}%</b></div>
				<i><em style={{ width: `${utilization}%` }} /></i>
				<small>Token {formatTokens(today?.total_tokens ?? 0)} / {formatTokens(today?.token_budget ?? 0)}；70% 提醒，90% 告警，100% 仅暂停新模型分析。</small>
			</div>

			<div className="token-analysis-grid">
				<div className="token-trend-card">
					<div className="token-subheading"><span><Layers3 size={14} />使用趋势</span><small>{report?.bucket === "day" ? "按日" : "按小时"} · 最近 {days} 天</small></div>
					<UsageChart points={report?.trend ?? []} bucket={report?.bucket ?? "hour"} />
				</div>
				<div className="token-dimensions">
					<DimensionList title="供应商" items={report?.by_provider ?? []} />
					<DimensionList title="模型" items={report?.by_model ?? []} />
					<DimensionList title="Agent" items={report?.by_agent ?? []} />
					<DimensionList title="币种" items={report?.by_symbol ?? []} />
					<DimensionList title="来源" items={report?.by_source ?? []} />
				</div>
			</div>

			<div className="token-recent">
				<div className="token-subheading"><span>最近调用明细</span><small>只保存统计元数据，不保存 Prompt 和回复正文</small></div>
				{report?.recent.length ? (
					<div className="table-wrap"><table><thead><tr><th>时间 / Run</th><th>供应商 / 模型</th><th>Agent / 币种</th><th>来源</th><th>Token</th><th>成本</th><th>耗时</th><th>状态</th></tr></thead><tbody>
						{report.recent.map((event) => <tr key={`${event.id}-${event.ts}`}>
							<td><strong>{new Date(event.ts).toLocaleString("zh-CN", { hour12: false })}</strong><small>{event.run_id || "未绑定运行"}</small></td>
							<td><strong>{event.provider}</strong><small>{event.model}</small></td>
							<td><strong>{event.agent || "未归因"}</strong><small>{event.symbol || "-"}</small></td>
							<td>{event.source === "automatic" ? "自动" : event.source === "manual" ? "人工" : event.source}</td>
							<td>{formatTokens(event.input_tokens + event.output_tokens)}{event.token_estimated ? <small>估算</small> : null}</td>
							<td>{formatUSD(event.cost_usd)}{event.cost_estimated ? <small>估算</small> : null}</td>
							<td>{event.duration_ms} ms</td>
							<td><span className={`token-status token-status--${event.status}`}>{statusLabel(event.status)}</span></td>
						</tr>)}
					</tbody></table></div>
				) : <EmptyState>当前范围内暂无调用明细。</EmptyState>}
			</div>
		</Panel>
	);
}
