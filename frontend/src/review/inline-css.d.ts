// Vite's ?inline import: the stylesheet's text, not a side effect.
declare module "*.css?inline" {
  const css: string;
  export default css;
}
