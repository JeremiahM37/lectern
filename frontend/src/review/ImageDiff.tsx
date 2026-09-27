import { useEffect, useState } from "react";
import { useStoredPref } from "./prefs";
import { t, useLocale } from "../i18n";

export type ImageSource = (path: string, side: "old" | "new") => Promise<string | undefined>;

type Mode = "side" | "swipe" | "onion";
const MODES: readonly Mode[] = ["side", "swipe", "onion"];
const LABELS = (): Record<Mode, string> => ({ side: t("review.image.side"), swipe: t("review.image.swipe"), onion: t("review.image.onion") });

/**
 * Before/after for a changed image: two up, a swipe divider, or onion skin
 * (the new image faded over the old). Images arrive as data: URLs from the
 * review API, so an SVG shown here can never run script. The mode is a
 * per-device preference.
 */
export function ImageDiff({ path, source }: { path: string; source: ImageSource }) {
  useLocale();
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

  if (!images) return <p className="sub img-diff-note">{t("review.image.loading")}</p>;
  if (images.error) return <p className="sub error img-diff-note">{images.error}</p>;
  const both = images.old && images.new;
  return (
    <div className="img-diff" data-mode={both ? mode : "side"}>
      {both && (
        <div className="img-diff-modes" role="group" aria-label={t("review.image.comparison")}>
          {MODES.map((m) => (
            <button
              key={m}
              type="button"
              className="b"
              aria-pressed={mode === m}
              onClick={() => setMode(m)}
            >
              {LABELS()[m]}
            </button>
          ))}
        </div>
      )}
      {(!both || mode === "side") && (
        <div className="img-diff-side">
          <figure>
            <figcaption>{t("review.image.before")}</figcaption>
            {images.old ? <img src={images.old} alt={t("review.image.altBefore", { path })} /> : <p className="sub">{t("review.image.newImage")}</p>}
          </figure>
          <figure>
            <figcaption>{t("review.image.after")}</figcaption>
            {images.new ? <img src={images.new} alt={t("review.image.altAfter", { path })} /> : <p className="sub">{t("review.image.deleted")}</p>}
          </figure>
        </div>
      )}
      {both && mode !== "side" && (
        <>
          <div className="img-diff-stack">
            <img src={images.old} alt={t("review.image.altBefore", { path })} />
            <img
              src={images.new}
              alt={t("review.image.altAfter", { path })}
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
            aria-label={mode === "swipe" ? t("review.image.swipePosition") : t("review.image.opacity")}
            onChange={(e) => setAmount(Number(e.target.value))}
          />
        </>
      )}
    </div>
  );
}
