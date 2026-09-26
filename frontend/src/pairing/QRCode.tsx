// Renders a QR code as inline SVG using the vendored, dependency-free encoder
// in qrcodegen.ts (Project Nayuki's QR Code generator library, MIT licensed —
// see that file's header). No npm dependency, no canvas, no network request:
// the whole thing is a handful of small filled squares, which an SVG <path>
// with one subpath per dark module renders crisply at any size.
import { qrcodegen } from "./qrcodegen";

export function QRCode({
  value,
  size = 200,
}: {
  value: string;
  size?: number;
}) {
  const qr = qrcodegen.QrCode.encodeText(value, qrcodegen.QrCode.Ecc.MEDIUM);
  const n = qr.size;
  let path = "";
  for (let y = 0; y < n; y++) {
    for (let x = 0; x < n; x++) {
      if (qr.getModule(x, y)) path += `M${x},${y}h1v1h-1z`;
    }
  }
  // A 1-module quiet border keeps real-world scanners happy; the spec calls
  // for 4, but this is shown right next to explanatory text, not printed on
  // its own, so 1 is enough not to look cramped against the code's edge.
  const border = 1;
  const view = n + border * 2;
  return (
    <svg
      viewBox={`-${border} -${border} ${view} ${view}`}
      width={size}
      height={size}
      role="img"
      aria-label="Pairing QR code — scan with your phone's camera"
      className="pairing-qr"
    >
      <rect x={-border} y={-border} width={view} height={view} fill="#fff" />
      <path d={path} fill="#000" />
    </svg>
  );
}
