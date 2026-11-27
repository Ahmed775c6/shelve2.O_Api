import { useId, useState } from "react";
import type { BreakdownPoint, ChartPoint } from "@/lib/admin-types";

const chartWidth = 640;
const chartHeight = 210;
const chartMargin = { left: 36, right: 12, top: 18, bottom: 31 };

export function TrendChart({ values, kind, color = "#bba4f4", unit = "requests" }: {
  values: ChartPoint[];
  kind: "area" | "line" | "bar";
  color?: string;
  unit?: string;
}) {
  const [activeIndex, setActiveIndex] = useState<number | null>(null);
  const gradientId = useId().replace(/:/g, "");
  if (!values.length) return <div className="chart-empty">No data recorded for this period.</div>;
  const innerWidth = chartWidth - chartMargin.left - chartMargin.right;
  const innerHeight = chartHeight - chartMargin.top - chartMargin.bottom;
  const max = Math.max(1, ...values.map((entry) => entry.count));
  const total = values.reduce((sum, entry) => sum + entry.count, 0);
  const x = (index: number) => chartMargin.left + (values.length === 1 ? innerWidth / 2 : index * innerWidth / (values.length - 1));
  const y = (count: number) => chartMargin.top + innerHeight - count / max * innerHeight;
  const coordinates = values.map((entry, index) => ({ x: x(index), y: y(entry.count) }));
  const line = smoothLinePath(coordinates);
  const area = `${line} L ${x(values.length - 1)},${chartMargin.top + innerHeight} L ${x(0)},${chartMargin.top + innerHeight} Z`;
  const barWidth = Math.min(42, innerWidth / values.length * 0.5);
  const sparkWidth = Math.min(14, innerWidth / values.length * 0.42);
  const labelStep = Math.max(1, Math.ceil((values.length - 1) / 8));
  const active = activeIndex === null ? null : values[activeIndex];
  const activePercent = active && total ? (active.count / total * 100).toFixed(1) : "0.0";
  const tooltipX = activeIndex === null ? 0 : Math.max(9, Math.min(91, x(activeIndex) / chartWidth * 100));
  const tooltipY = activeIndex === null ? 0 : Math.max(16, y(values[activeIndex].count) / chartHeight * 100);
  return <div className="trend-chart-wrap" onMouseLeave={() => setActiveIndex(null)}>
    <svg className="trend-chart" viewBox={`0 0 ${chartWidth} ${chartHeight}`} role="img" aria-label={`${kind} chart of ${unit}`}>
      <defs>
        <linearGradient id={`${gradientId}-area`} x1="0" x2="0" y1="0" y2="1">
          <stop offset="0%" stopColor={color} stopOpacity=".26" />
          <stop offset="72%" stopColor={color} stopOpacity=".055" />
          <stop offset="100%" stopColor={color} stopOpacity="0" />
        </linearGradient>
        <linearGradient id={`${gradientId}-bar`} x1="0" x2="0" y1="0" y2="1">
          <stop offset="0%" stopColor={color} stopOpacity=".96" />
          <stop offset="100%" stopColor={color} stopOpacity=".54" />
        </linearGradient>
      </defs>
      {[0, 0.25, 0.5, 0.75, 1].map((fraction) => <g key={fraction}>
        <line x1={chartMargin.left} x2={chartWidth - chartMargin.right} y1={chartMargin.top + innerHeight * fraction} y2={chartMargin.top + innerHeight * fraction} className="chart-grid-line" />
        <text x={chartMargin.left - 8} y={chartMargin.top + innerHeight * fraction + 4} textAnchor="end" className="chart-axis-label">{Math.round(max * (1 - fraction)).toLocaleString()}</text>
      </g>)}
      {kind === "area" && values.map((entry, index) => <rect
        key={`spark-${entry.date}`}
        className="chart-spark-column"
        x={x(index) - sparkWidth / 2}
        y={y(entry.count)}
        width={sparkWidth}
        height={chartMargin.top + innerHeight - y(entry.count)}
        rx="2"
        fill={color}
      />)}
      {kind === "bar" ? values.map((entry, index) => {
        const barHeight = Math.max(3, innerHeight - (y(entry.count) - chartMargin.top));
        const percent = total ? (entry.count / total * 100).toFixed(1) : "0.0";
        const label = `${entry.date}: ${entry.count.toLocaleString()} ${unit}, ${percent}% of selected range`;
        return <g key={entry.date} tabIndex={0} role="img" aria-label={label} onFocus={() => setActiveIndex(index)} onBlur={() => setActiveIndex(null)} onMouseEnter={() => setActiveIndex(index)}>
          <rect className="chart-bar-track" x={x(index) - barWidth / 2} y={chartMargin.top} width={barWidth} height={innerHeight} rx="7" />
          <rect className="chart-bar-value" x={x(index) - barWidth / 2} y={chartMargin.top + innerHeight - barHeight} width={barWidth} height={barHeight} rx="7" fill={`url(#${gradientId}-bar)`} opacity={activeIndex === index ? "1" : ".8"} />
          <title>{label}</title>
        </g>;
      }) : <>
        {kind === "area" && <path d={area} fill={`url(#${gradientId}-area)`} />}
        <path className="chart-series-line" d={line} fill="none" stroke={color} strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" />
        {values.map((entry, index) => {
          const percent = total ? (entry.count / total * 100).toFixed(1) : "0.0";
          const label = `${entry.date}: ${entry.count.toLocaleString()} ${unit}, ${percent}% of selected range`;
          return <g key={entry.date} tabIndex={0} role="img" aria-label={label} onFocus={() => setActiveIndex(index)} onBlur={() => setActiveIndex(null)} onMouseEnter={() => setActiveIndex(index)}>
            <circle className="chart-data-point" cx={x(index)} cy={y(entry.count)} r={activeIndex === index ? "4.5" : "0"} fill={color} />
            <circle className="chart-hit-area" cx={x(index)} cy={y(entry.count)} r="11" />
            <title>{label}</title>
          </g>;
        })}
      </>}
      {activeIndex !== null && kind !== "bar" && <line className="chart-crosshair" x1={x(activeIndex)} x2={x(activeIndex)} y1={chartMargin.top} y2={chartMargin.top + innerHeight} />}
      {values.map((entry, index) => (index % labelStep === 0 || index === values.length - 1) && <text key={entry.date} x={x(index)} y={chartHeight - 8} textAnchor="middle" className="chart-axis-label">
        {formatPeriod(entry.date, values.length)}
      </text>)}
    </svg>
    {active && <div className="trend-chart-tooltip" role="status" style={{ left: `${tooltipX}%`, top: `${tooltipY}%` }}>
      <strong>{active.date}</strong>
      <span>{active.count.toLocaleString()} {unit}</span>
      <small>{activePercent}% of selected range</small>
    </div>}
  </div>;
}

function smoothLinePath(points: { x: number; y: number }[]) {
  if (points.length < 2) {
    const point = points[0];
    return point ? `M ${point.x},${point.y}` : "";
  }
  return points.reduce((path, point, index) => {
    if (index === 0) return `M ${point.x},${point.y}`;
    const previous = points[index - 1];
    const midpoint = (previous.x + point.x) / 2;
    return `${path} C ${midpoint},${previous.y} ${midpoint},${point.y} ${point.x},${point.y}`;
  }, "");
}

export function RadarChart({ values }: { values: BreakdownPoint[] }) {
  const data = values.slice(0, 6);
  if (!data.length) return <div className="chart-empty">No platform data available.</div>;
  const size = 240;
  const center = size / 2;
  const radius = 78;
  const max = Math.max(1, ...data.map((item) => item.count));
  const total = data.reduce((sum, item) => sum + item.count, 0);
  const point = (index: number, ratio: number) => {
    const angle = -Math.PI / 2 + index * Math.PI * 2 / data.length;
    return `${center + Math.cos(angle) * radius * ratio},${center + Math.sin(angle) * radius * ratio}`;
  };
  const polygon = (ratio: number) => data.map((_, index) => point(index, ratio)).join(" ");
  return <svg className="radar-chart" viewBox={`0 0 ${size} ${size}`} role="img" aria-label="User platform radar chart">
    {[0.25, 0.5, 0.75, 1].map((ratio) => <polygon key={ratio} points={polygon(ratio)} className="radar-grid" />)}
    <polygon points={data.map((item, index) => point(index, item.count / max)).join(" ")} className="radar-value" />
    {data.map((item, index) => {
      const angle = -Math.PI / 2 + index * Math.PI * 2 / data.length;
      const cx = center + Math.cos(angle) * radius * (item.count / max);
      const cy = center + Math.sin(angle) * radius * (item.count / max);
      return <circle key={`point-${item.label || item.platform}`} cx={cx} cy={cy} r="4" className="radar-dot">
        <title>{`${item.label || item.platform}: ${item.count.toLocaleString()} users · ${total ? (item.count / total * 100).toFixed(1) : "0.0"}%`}</title>
      </circle>;
    })}
    {data.map((item, index) => {
      const angle = -Math.PI / 2 + index * Math.PI * 2 / data.length;
      const labelX = center + Math.cos(angle) * (radius + 23);
      const labelY = center + Math.sin(angle) * (radius + 23);
      return <text key={item.label || item.platform} x={labelX} y={labelY + 3} textAnchor="middle" className="chart-axis-label">{item.label || item.platform}</text>;
    })}
  </svg>;
}

const pieColors = ["#bba4f4", "#f0d16a", "#6ed6b3", "#f18f91", "#83a8ff"];

function arcPath(start: number, end: number, radius: number) {
  const pointAt = (angle: number) => [120 + radius * Math.cos(angle), 120 + radius * Math.sin(angle)];
  const [startX, startY] = pointAt(start);
  const [endX, endY] = pointAt(end);
  const largeArc = end - start > Math.PI ? 1 : 0;
  return `M 120 120 L ${startX} ${startY} A ${radius} ${radius} 0 ${largeArc} 1 ${endX} ${endY} Z`;
}

export function PieChart({ values }: { values: BreakdownPoint[] }) {
  const data = values.filter((item) => item.count > 0);
  const [activeIndex, setActiveIndex] = useState<number | null>(null);
  const total = data.reduce((sum, item) => sum + item.count, 0);
  if (!total) return <div className="chart-empty">No request status data available.</div>;
  const active = activeIndex === null ? null : data[activeIndex];
  return <div className="pie-chart-wrap">
    <svg className="pie-chart" viewBox="0 0 240 240" role="img" aria-label="Request response status pie chart">
      {data.length === 1
        ? <circle className="pie-slice" cx="120" cy="120" r="90" fill={pieColors[0]} tabIndex={0} aria-label={`${data[0]?.label || data[0]?.platform}: ${data[0]?.count.toLocaleString()} requests · 100%`} onMouseEnter={() => setActiveIndex(0)} onFocus={() => setActiveIndex(0)} onBlur={() => setActiveIndex(null)}><title>{`${data[0]?.label || data[0]?.platform}: ${data[0]?.count.toLocaleString()} requests · 100%`}</title></circle>
        : data.map((item, index) => {
        const startAngle = -Math.PI / 2 + data.slice(0, index).reduce((sum, entry) => sum + entry.count / total * Math.PI * 2, 0);
        const nextAngle = startAngle + item.count / total * Math.PI * 2;
        const path = arcPath(startAngle, nextAngle, 90);
        return <path key={item.label || item.platform} className="pie-slice" d={path} fill={pieColors[index % pieColors.length]} stroke="var(--surface)" strokeWidth="3" tabIndex={0} aria-label={`${item.label || item.platform}: ${item.count.toLocaleString()} requests · ${(item.count / total * 100).toFixed(1)}%`} onMouseEnter={() => setActiveIndex(index)} onFocus={() => setActiveIndex(index)} onBlur={() => setActiveIndex(null)}>
          <title>{`${item.label || item.platform}: ${item.count.toLocaleString()} requests · ${(item.count / total * 100).toFixed(1)}%`}</title>
        </path>;
      })}
      <circle cx="120" cy="120" r="52" fill="var(--surface)" />
      <text x="120" y="114" textAnchor="middle" className="pie-total">{(active?.count ?? total).toLocaleString()}</text>
      <text x="120" y="133" textAnchor="middle" className="chart-axis-label">{active ? active.label || active.platform : "requests"}</text>
      {active && <text x="120" y="148" textAnchor="middle" className="pie-active-percent">{(active.count / total * 100).toFixed(1)}%</text>}
    </svg>
    <div className="pie-legend">{data.map((item, index) => <div key={item.label || item.platform} title={`${item.count.toLocaleString()} requests · ${(item.count / total * 100).toFixed(1)}%`}><i style={{ background: pieColors[index % pieColors.length] }} /><span>{item.label || item.platform}</span><strong>{item.count.toLocaleString()} <small>{(item.count / total * 100).toFixed(1)}%</small></strong></div>)}</div>
  </div>;
}

const bubbleColors = [
  { fill: "#bba4f4", ink: "#f0e9ff" },
  { fill: "#5bd0e1", ink: "#102b36" },
  { fill: "#f0d16a", ink: "#3d3212" },
  { fill: "#70d7b6", ink: "#12382d" },
  { fill: "#f18f91", ink: "#421f2a" },
  { fill: "#83a8ff", ink: "#18294c" },
  { fill: "#d3a8df", ink: "#352039" },
];

export function AgeBubbleChart({ values }: { values: BreakdownPoint[] }) {
  const data = values.filter((item) => item.count >= 0);
  const total = data.reduce((sum, item) => sum + item.count, 0);
  const max = Math.max(1, ...data.map((item) => item.count));
  if (!data.length) return <div className="chart-empty">No age data available.</div>;
  return <div className="age-bubble-chart">
    {data.map((item, index) => {
      const percent = total ? item.count / total * 100 : 0;
      const size = Math.round(54 + Math.sqrt(item.count / max) * 58);
      const palette = bubbleColors[index % bubbleColors.length];
      return <div
        className="age-bubble"
        key={item.label}
        style={{ width: `${size}px`, height: `${size}px`, backgroundColor: palette.fill, color: palette.ink }}
        title={`${item.label}: ${item.count.toLocaleString()} users · ${percent.toFixed(1)}%`}
        aria-label={`${item.label}: ${item.count.toLocaleString()} users · ${percent.toFixed(1)}%`}
        tabIndex={0}
      >
        <strong>{percent.toFixed(0)}%</strong>
        <span>{item.label}</span>
      </div>;
    })}
  </div>;
}

export function BreakdownChart({ values, unit = "users" }: { values: BreakdownPoint[]; unit?: string }) {
  const data = values.filter((item) => item.count >= 0);
  const total = data.reduce((sum, item) => sum + item.count, 0);
  const max = Math.max(1, ...data.map((item) => item.count));
  if (!data.length) return <div className="chart-empty">No data available.</div>;
  return <div className="breakdown-chart">
    {data.map((item) => {
      const percent = total ? item.count / total * 100 : 0;
      return <div className="breakdown-row" key={item.label} title={`${item.label}: ${item.count.toLocaleString()} ${unit} · ${percent.toFixed(1)}%`}>
        <div><span>{item.label}</span><strong>{item.count.toLocaleString()} <small>{percent.toFixed(1)}%</small></strong></div>
        <i><b style={{ width: `${Math.min(100, item.count / max * 100)}%` }} /></i>
      </div>;
    })}
  </div>;
}

function formatPeriod(value: string, count: number) {
  if (/^\d{4}$/.test(value)) return value;
  if (/^\d{4}-\d{2}$/.test(value)) {
    const date = new Date(`${value}-01T00:00:00Z`);
    return date.toLocaleDateString(undefined, { month: "short", year: count > 12 ? "2-digit" : undefined });
  }
  const date = new Date(`${value}T00:00:00Z`);
  return date.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}
