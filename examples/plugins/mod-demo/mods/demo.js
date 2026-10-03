// An example mod (docs/mods.md). The same file runs in the web app and in
// `lectern console`.
export function register(on) {
  on("app.start", async ($, e, next) => {
    $.ui.status("demo mod on");
    $.command.register({
      id: "counter",
      title: "Mod demo: open the counter",
      run: ($) => $.ui.open({ id: "counter", title: "Counter" }),
    });
    return next(e);
  });

  // A badge on every session: its number and agent.
  on("ui.render", "session.card", async ($, e, next) => {
    const out = await next(e);
    const { Badge } = $.ui.resolve(e);
    const s = e.props.session;
    out.append.push(Badge({ text: `#${s.id} ${s.agent}`, tone: "accent" }));
    return out;
  });

  // The pane the command opens. $.state survives reloads.
  on("ui.render", "pane", async ($, e, next) => {
    const out = await next(e);
    if (e.props.id !== "counter") return out;
    const { Box, Text, Button } = $.ui.resolve(e);
    const n = $.state.get("count") ?? 0;
    out.append.push(Box({ direction: "column", gap: 2, children: [
      Text({ text: `Pressed ${n} times`, bold: true }),
      Button({ label: "Press", hotkey: "p", onPress: () => $.state.set("count", n + 1) }),
    ] }));
    return out;
  });

  // A guard on what you send: refuse wiping the disk, and "shout:" upper-cases.
  on("prompt.submit", async ($, e, next) => {
    if (/\brm\s+-rf\s+\/(\s|$)/.test(e.text)) return { deny: "Mod demo: that would delete the whole disk." };
    if (e.text.startsWith("shout:")) return next({ ...e, text: e.text.slice(6).trim().toUpperCase() });
    return next(e);
  });
}
