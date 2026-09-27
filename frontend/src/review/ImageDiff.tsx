import { useEffect, useState } from "react";
import { useStoredPref } from "./prefs";

export type ImageSource = (path: string, side: "old" | "new") => Promise<string | undefined>;

type Mode = "side" | "swipe" | "onion";
const MODES: readonly Mode[] = ["side", "swipe", "onion"];
const LABELS: Record<Mode, string> = { side: "2-up", swipe: "Swipe", onion: "Onion skin" };

/**
 * Before/after for a changed image: two up, a swipe divider, or onion skin
 * (the new image faded over the old). Images arrive as data: URLs from the
 * review API, so an SVG shown here can never run script. The mode is a
 * per-device preference.
 */
export function ImageDiff({ path, source }: { path: string; source: ImageSource }) {
  const [mode, setMode] = useStoredPref<Mode>("lec-imagediff", "side", MODES);
  const [images, setImages] = useState<{ old?: string; new?: string; error?: string }>();
  const [amount, setAmount] = useState(50);
  useEffect(() => {
    let live = true;
    setImages(undefined);
    Promise.all([source(path, "old"), source(path, "new")])
      .then(([o, n]) => live && setImages({ old: o, new: n }))
      .catch((e) => live && setImages({ error: String(e) }));
    return () => {
      live = false;
    };
  }, [path, source]);

  if (!images) return <p className="sub img-diff-note">Loading images…</p>;
  if (images.error) return <p className="sub error img-diff-note">{images.error}</p>;
  const both = images.old && images.new;
  return (
    <div className="img-diff" data-mode={both ? mode : "side"}>
      {both && (
        <div className="img-diff-modes" role="group" aria-label="Image comparison">
          {MODES.map((m) => (
            <button
              key={m}
              type="button"
              className="b"
              aria-pressed={mode === m}
              onClick={() => setMode(m)}
            >
              {LABELS[m]}
            </button>
          ))}
        </div>
      )}
      {(!both || mode === "side") && (
        <div className="img-diff-side">
          <figure>
            <figcaption>Before</figcaption>
            {images.old ? <img src={images.old} alt={`${path} before`} /> : <p className="sub">New image</p>}
          </figure>
          <figure>
            <figcaption>After</figcaption>
            {images.new ? <img src={images.new} alt={`${path} after`} /> : <p className="sub">Deleted</p>}
          </figure>
        </div>
      )}
      {both && mode !== "side" && (
        <>
          <div className="img-diff-stack">
            <img src={images.old} alt={`${path} before`} />
            <img
              src={images.new}
              alt={`${path} after`}
              className="img-diff-top"
              style={
                mode === "swipe"
                  ? { clipPath: `inset(0 0 0 ${amount}%)` }
                  : { opacity: amount / 100 }
              }
            />
            {mode === "swipe" && <span className="img-diff-divider" style={{ left: `${amount}%` }} />}
          </div>
          <input
            type="range"
            min={0}
            max={100}
            value={amount}
            aria-label={mode === "swipe" ? "Swipe position" : "Opacity of the new image"}
            onChange={(e) => setAmount(Number(e.target.value))}
          />
        </>
      )}
    </div>
  );
}
