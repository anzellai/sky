export function npmNameToSkyModule(name: string): string {

  const clean =
    name
      .replace(/^@/, "")
      .replace("/", "_")

  return clean
    .split(/[-_]/g)
    .map(x => x.charAt(0).toUpperCase() + x.slice(1))
    .join("")

}
