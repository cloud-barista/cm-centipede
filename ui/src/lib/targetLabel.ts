// How a destination writes its own name.
//
//   beetleSsh cpbt01 / web-tier / web-02
//   ssh fs-target
//
// One kind word, one space, then whatever identifies the thing. The kind comes
// from ConnectionRef.source, so the label says which arm of the reference is
// filled — `beetleDb` and `db` are different destinations reached differently,
// and a label that dropped that would make them look alike.
//
// Written in two places (the cm-beetle picker and the wizard's inline mode) and
// read in two more, which is how the inline one drifted to "ssh · fs-target"
// and stopped splitting. Both ends live here now so a change is one edit.

/** The name a destination is listed and remembered under. */
export function targetLabel(kind: string, name: string): string {
  return `${kind} ${name}`;
}

/**
 * The label back into its two parts, for drawing them differently — the kind
 * muted, the name in full contrast.
 *
 * A label with no space is all kind and no name: that is what an older stored
 * label, or one built by hand, may be, and returning it as the kind keeps it
 * visible rather than blank.
 */
export function splitTargetLabel(label: string): [kind: string, name: string] {
  const at = label.indexOf(' ');
  return at === -1 ? [label, ''] : [label.slice(0, at), label.slice(at + 1)];
}
