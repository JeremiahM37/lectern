import { useEffect, useState } from "react";

const PHONE = "(max-width: 600px)";

/** Whether the view is phone-width (the same breakpoint as shell/mobile.css). */
export function usePhone(): boolean {
  const [phone, setPhone] = useState(() => typeof matchMedia === "function" && matchMedia(PHONE).matches);
  useEffect(() => {
    if (typeof matchMedia !== "function") return;
    const media = matchMedia(PHONE);
    const update = () => setPhone(media.matches);
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);
  return phone;
}
