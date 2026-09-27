import { t } from "../i18n";
import { useEffect, useRef, useState } from "react";
import { errorMessage } from "../terminal/model";

// Images: fit to the pane by default; zoom with the buttons, Ctrl+wheel or a
// two-finger pinch, and pan by scrolling.
export function ImageView({ url, name }: { url: string; name: string }) {
  const [zoom, setZoom] = useState<number | "fit">("fit");
  const [natural, setNatural] = useState<{ w: number; h: number }>();
  const box = useRef<HTMLDivElement>(null);
  const pinch = useRef(new Map<number, { x: number; y: number }>());
  const start = useRef<{ distance: number; zoom: number }>(undefined);
  const current = () => {
    if (zoom !== "fit") return zoom;
    const element = box.current,
      size = natural;
    if (!element || !size) return 1;
    return Math.min(1, (element.clientWidth - 24) / size.w, (element.clientHeight - 24) / size.h);
  };
  const set = (value: number) => setZoom(Math.max(0.05, Math.min(16, value)));
  useEffect(() => {
    const element = box.current;
    if (!element) return;
    const wheel = (event: WheelEvent) => {
      if (!event.ctrlKey && !event.metaKey) return;
      event.preventDefault();
      set(current() * (event.deltaY < 0 ? 1.15 : 1 / 1.15));
    };
    element.addEventListener("wheel", wheel, { passive: false });
    return () => element.removeEventListener("wheel", wheel);
  });
  const scale = current();
  return (
    <div className="wb-image">
      <div className="wb-image-tools" role="toolbar" aria-label={t("files.zoom")}>
        <button aria-label={t("files.zoomOut")} onClick={() => set(scale / 1.25)}>−</button>
        <button aria-pressed={zoom === "fit"} onClick={() => setZoom("fit")}>{t("files.fit")}</button>
        <button aria-pressed={zoom === 1} onClick={() => setZoom(1)}>100%</button>
        <button aria-label={t("files.zoomIn")} onClick={() => set(scale * 1.25)}>+</button>
        <span className="wb-image-size">
          {natural ? `${natural.w} × ${natural.h} · ${Math.round(scale * 100)}%` : ""}
        </span>
      </div>
      <div
        ref={box}
        className="wb-image-stage"
        onPointerDown={(event) => {
          if (event.pointerType !== "touch") return;
          pinch.current.set(event.pointerId, { x: event.clientX, y: event.clientY });
          if (pinch.current.size === 2) {
            const [a, b] = [...pinch.current.values()];
            start.current = { distance: Math.hypot(a!.x - b!.x, a!.y - b!.y), zoom: scale };
          }
        }}
        onPointerMove={(event) => {
          if (!pinch.current.has(event.pointerId)) return;
          pinch.current.set(event.pointerId, { x: event.clientX, y: event.clientY });
          if (pinch.current.size === 2 && start.current) {
            const [a, b] = [...pinch.current.values()];
            set(start.current.zoom * (Math.hypot(a!.x - b!.x, a!.y - b!.y) / start.current.distance));
          }
        }}
        onPointerUp={(event) => {
          pinch.current.delete(event.pointerId);
          if (pinch.current.size < 2) start.current = undefined;
        }}
        onPointerCancel={(event) => pinch.current.delete(event.pointerId)}
        onDoubleClick={() => setZoom(zoom === "fit" ? 1 : "fit")}
      >
        <img
          src={url}
          alt={name}
          draggable={false}
          onLoad={(event) => setNatural({ w: event.currentTarget.naturalWidth, h: event.currentTarget.naturalHeight })}
          style={natural ? { width: natural.w * scale, height: natural.h * scale } : undefined}
        />
      </div>
    </div>
  );
}

// PDF.js ships in the binary under /vendor (docs/terminal-workspace.md).
type PDFPage = {
  getViewport: (options: { scale: number }) => { width: number; height: number };
  render: (options: { canvasContext: CanvasRenderingContext2D; viewport: unknown; transform?: number[] }) => { promise: Promise<void>; cancel: () => void };
};
type PDFDocument = { numPages: number; getPage: (page: number) => Promise<PDFPage>; destroy: () => Promise<void> };
type PDFModule = {
  GlobalWorkerOptions: { workerSrc: string };
  getDocument: (options: { data: Uint8Array; standardFontDataUrl: string; cMapUrl: string; cMapPacked: boolean; isEvalSupported: boolean }) => {
    promise: Promise<PDFDocument>;
    destroy: () => Promise<void>;
  };
};

const PDF_POSITIONS = "lec-pdf-positions";
interface PdfPosition {
  page: number;
  zoom: number;
  scroll: number;
}
function loadPosition(key: string): PdfPosition | undefined {
  try {
    return (JSON.parse(localStorage.getItem(PDF_POSITIONS) || "{}") as Record<string, PdfPosition>)[key];
  } catch {
    return undefined;
  }
}
function savePosition(key: string, position: PdfPosition) {
  try {
    const all = JSON.parse(localStorage.getItem(PDF_POSITIONS) || "{}") as Record<string, PdfPosition & { at?: number }>;
    all[key] = { ...position, at: Date.now() } as PdfPosition;
    // Remember the 100 most recent documents.
    const kept = Object.entries(all)
      .sort((a, b) => ((b[1] as { at?: number }).at || 0) - ((a[1] as { at?: number }).at || 0))
      .slice(0, 100);
    localStorage.setItem(PDF_POSITIONS, JSON.stringify(Object.fromEntries(kept)));
  } catch {}
}

/** A PDF, one page at a time; reopening it returns to the same page and scroll. */
export function PdfView({ blob, positionKey }: { blob: Blob; positionKey: string }) {
  const canvas = useRef<HTMLCanvasElement>(null),
    stage = useRef<HTMLDivElement>(null);
  const saved = useRef(loadPosition(positionKey));
  const [pdf, setPDF] = useState<PDFDocument>();
  const [page, setPage] = useState(saved.current?.page || 1);
  const [zoom, setZoom] = useState(saved.current?.zoom || 1);
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState("");
  const restored = useRef(false);
  useEffect(() => {
    let closed = false,
      task: ReturnType<PDFModule["getDocument"]> | undefined;
    void (async () => {
      const vendor = "/vendor/pdf.mjs";
      const module = (await import(/* @vite-ignore */ vendor)) as PDFModule;
      if (closed) return;
      module.GlobalWorkerOptions.workerSrc = "/vendor/pdf.worker.mjs";
      task = module.getDocument({
        data: new Uint8Array(await blob.arrayBuffer()),
        standardFontDataUrl: "/vendor/standard_fonts/",
        cMapUrl: "/vendor/cmaps/",
        cMapPacked: true,
        isEvalSupported: false,
      });
      const document = await task.promise;
      if (closed) {
        await document.destroy();
        return;
      }
      setPage((old) => Math.min(Math.max(1, old), document.numPages));
      setPDF(document);
    })().catch((error) => {
      if (!closed) {
        setError(errorMessage(error));
        setBusy(false);
      }
    });
    return () => {
      closed = true;
      void task?.destroy();
    };
  }, [blob]);
  useEffect(() => {
    if (!pdf || !canvas.current) return;
    let closed = false,
      render: ReturnType<PDFPage["render"]> | undefined;
    setBusy(true);
    void (async () => {
      const source = await pdf.getPage(page),
        element = canvas.current;
      if (closed || !element) return;
      const original = source.getViewport({ scale: 1 }),
        width = Math.max(280, Math.min(1000, (stage.current?.clientWidth || 800) - 8)) * zoom,
        viewport = source.getViewport({ scale: width / original.width }),
        ratio = Math.min(devicePixelRatio || 1, 2);
      element.width = Math.floor(viewport.width * ratio);
      element.height = Math.floor(viewport.height * ratio);
      element.style.width = viewport.width + "px";
      const context = element.getContext("2d");
      if (!context) throw new Error("Canvas unavailable");
      render = source.render({ canvasContext: context, viewport, transform: [ratio, 0, 0, ratio, 0, 0] });
      await render.promise;
      if (!restored.current && stage.current) {
        restored.current = true;
        stage.current.scrollTop = saved.current?.scroll || 0;
      }
    })()
      .catch((error) => {
        if (!closed) setError(errorMessage(error));
      })
      .finally(() => {
        if (!closed) setBusy(false);
      });
    return () => {
      closed = true;
      render?.cancel();
    };
  }, [pdf, page, zoom]);
  useEffect(() => {
    if (pdf) savePosition(positionKey, { page, zoom, scroll: stage.current?.scrollTop || 0 });
  }, [pdf, page, zoom, positionKey]);
  const go = (next: number) => {
    if (stage.current) stage.current.scrollTop = 0;
    setPage(next);
  };
  return (
    <div className="wb-pdf">
      <div className="pdf-controls">
        <button disabled={busy || page <= 1} onClick={() => go(page - 1)}>
          {t("files.prevPage")}
        </button>
        <span id="pdf-page">{error || (!busy && pdf ? t("files.pageOf", { page, pages: pdf.numPages }) : t("files.loadingPdf"))}</span>
        <button disabled={busy || !pdf || page >= pdf.numPages} onClick={() => go(page + 1)}>
          {t("files.nextPage")}
        </button>
        <button aria-label={t("files.zoomOut")} disabled={zoom <= 0.5} onClick={() => setZoom(Math.max(0.5, zoom / 1.25))}>−</button>
        <button aria-label={t("files.zoomIn")} disabled={zoom >= 4} onClick={() => setZoom(Math.min(4, zoom * 1.25))}>+</button>
      </div>
      <div
        ref={stage}
        className="wb-pdf-stage"
        onScroll={(event) => {
          if (pdf && restored.current) savePosition(positionKey, { page, zoom, scroll: event.currentTarget.scrollTop });
        }}
      >
        <canvas ref={canvas} aria-label={t("files.pdfPage")} />
      </div>
    </div>
  );
}
