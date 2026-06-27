/**
 * Health probe for k8s liveness/readiness. Returns immediately; does NOT call
 * the backend or check any external dependencies (we want pod restarts only
 * when the Node.js process itself is wedged, not when the backend is down).
 */
export const GET = () => Response.json({ status: "ok" });
