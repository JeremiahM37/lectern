// Vite turns an import ending in ?worker into a Worker constructor for a
// separately bundled file.
declare module "*?worker" {
  const WorkerConstructor: new () => Worker;
  export default WorkerConstructor;
}
