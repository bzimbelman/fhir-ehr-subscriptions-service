import type { SparklinePoint } from "@/lib/interfaces";

/**
 * Lightweight inline-SVG sparkline. Chose zero deps over Recharts because:
 *   - this is a single 24-bucket polyline, not a full chart;
 *   - Recharts pulls in d3-scale + d3-shape + react-smooth (~50kB gzip);
 *   - tests are easier when the rendered markup is plain SVG.
 *
 * The container is responsive: width fills the parent (viewBox-based
 * scaling). Height is fixed at 60px so it sits nicely in a header card.
 *
 * Render contract for tests: each datum becomes one `<circle>` with
 * `data-testid="sparkline-point"` so a test can count them. The full
 * polyline is also testable via `data-testid="sparkline-polyline"`.
 */

interface SparklineProps {
  points: SparklinePoint[];
  /** Aria-label override for accessibility (default: "throughput sparkline"). */
  label?: string;
}

const VB_WIDTH = 240;
const VB_HEIGHT = 60;
const PAD = 4;

export function Sparkline({ points, label }: SparklineProps) {
  const dataPoints =
    points.length === 0
      ? [{ bucketStart: "empty", total: 0 }]
      : points;
  const max = Math.max(1, ...dataPoints.map((p) => p.total));
  const innerW = VB_WIDTH - 2 * PAD;
  const innerH = VB_HEIGHT - 2 * PAD;

  const xs = dataPoints.map((_p, i) => {
    if (dataPoints.length === 1) return PAD + innerW / 2;
    return PAD + (i / (dataPoints.length - 1)) * innerW;
  });
  const ys = dataPoints.map(
    (p) => VB_HEIGHT - PAD - (p.total / max) * innerH,
  );

  const polylinePoints = dataPoints
    .map((_, i) => `${xs[i]},${ys[i]}`)
    .join(" ");

  return (
    <svg
      role="img"
      aria-label={label ?? "throughput sparkline"}
      data-testid="sparkline"
      viewBox={`0 0 ${VB_WIDTH} ${VB_HEIGHT}`}
      preserveAspectRatio="none"
      className="h-16 w-full"
    >
      <polyline
        data-testid="sparkline-polyline"
        fill="none"
        stroke="#2563eb"
        strokeWidth="2"
        points={polylinePoints}
      />
      {points.length === 0 ? null : (
        <>
          {dataPoints.map((p, i) => (
            <circle
              key={p.bucketStart}
              data-testid="sparkline-point"
              data-bucket={p.bucketStart}
              data-total={p.total}
              cx={xs[i]}
              cy={ys[i]}
              r={1.6}
              fill="#2563eb"
            />
          ))}
        </>
      )}
    </svg>
  );
}
