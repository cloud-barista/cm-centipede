'use client';

// One source connection's mapping in a path domain: which scanned unit moves,
// where to, and the ordered include/exclude rules over it.

import React from 'react';
import { Field, Note, Radio, Select, TextInput } from '@/components/ui/primitives';
import { PathRuleEditor } from '@/components/filter/PathRuleEditor';
import type { SourceUnit } from '@/components/domains/adapters';
import type { MappingDraft } from '@/store/wizardStore';
import { categoryOfRef, type Category } from '@/lib/connectionCategory';
import { beetleOsIdOf, prefixAfterBucket } from '@/lib/bucketPath';
import type { ConnectionRef, Strategy } from '@/types/common';

/**
 * Why this card's target path cannot be planned for a cross-storage move, or
 * undefined when it can — or when the move is not cross-storage.
 *
 * Within one storage type an empty target is the source path used as-is, which
 * means the same thing on both sides. Across them it means nothing sensible, so
 * it is not accepted here — the server, sent one, would still fill it in with
 * the source path:
 *
 *   object storage → file system   "my-bucket" is a relative path, so the files
 *                                  land under the SSH user's home directory.
 *   file system → object storage   "/root/testdata" is split into bucket and
 *                                  prefix, so a minio or honeybee target writes
 *                                  to a bucket named "root". (A cm-beetle target
 *                                  replaces the bucket with its osId and fills
 *                                  the bucket's root, which at least works.)
 *
 * So across storage types the target is asked for, in the destination's own
 * form: an absolute path for a file system, bucket/prefix/ for object storage.
 */
export function crossTargetProblem(
  category: Category,
  target: ConnectionRef | null,
  dstPath: string,
): string | undefined {
  const dstKind = categoryOfRef(target);
  if (!target || !dstKind || dstKind === category) return undefined;
  const path = dstPath.trim();
  // What is asked for, not what an empty value would have done: the field
  // cannot be left empty here, so describing that outcome only reads as an
  // option. The reasons stay in this comment.
  if (dstKind === 'filesystem') {
    if (!path) return 'enter an absolute path on the target host, e.g. /data/restored';
    if (!path.startsWith('/')) return 'must be an absolute path, starting with /';
    return undefined;
  }
  // A cm-beetle bucket is fixed to its osId and only the prefix is typed, so
  // there is no first segment to get wrong and an empty prefix is the root.
  if (beetleOsIdOf(target)) return undefined;
  if (!path) return 'enter bucket/prefix/ in the target';
  if (path.startsWith('/')) return 'must start with the bucket name, without a leading /';
  return undefined;
}

export function Step3PathMapping({
  category,
  connId,
  connName,
  units,
  target,
  mapping,
  shared,
  onChange,
}: {
  category: Category;
  connId: string;
  connName: string;
  /** The units of this connection's scan. */
  units: SourceUnit[];
  target: ConnectionRef | null;
  mapping: MappingDraft;
  /** Other selected connections writing to the same target. */
  shared: string[];
  onChange: (patch: Partial<MappingDraft>) => void;
}) {
  const dstKind = categoryOfRef(target);
  // Strategy picks how SSH reaches SSH, and whether a bucket streams to a
  // bucket or stages here first. Across storage types there is nothing for it
  // to choose between: those always relay.
  const showStrategy =
    (category === 'filesystem' && dstKind === 'filesystem') ||
    (category === 'objectstorage' && dstKind === 'objectstorage');
  // auto is left off for object storage: it resolves to direct there, so it
  // would only be a second name for the default.
  const strategies: Strategy[] =
    category === 'objectstorage' ? ['direct', 'relay'] : ['relay', 'direct', 'auto'];
  const strategyHint = (s: Strategy) =>
    category === 'objectstorage'
      ? s === 'direct'
        ? 'default — streams each object, nothing staged'
        : 'downloads to this server, then uploads'
      : s === 'relay'
        ? 'relays through this server'
        : s === 'direct'
          ? 'default — agent-forward, SSH→SSH only'
          : 'data-mig picks';

  // Two sources on one target need a path each, or the server refuses the
  // second. The example says so by naming this connection; a target used by
  // one source keeps the plain example, where the source path as-is is right.
  // A cm-beetle bucket is its osId, shown fixed in front of the field: only the
  // prefix after it is the reader's — see lib/bucketPath.
  const osId = dstKind === 'objectstorage' ? beetleOsIdOf(target) : undefined;
  const placeholder = osId
    ? shared.length > 0
      ? `ex) ${connName}/`
      : 'ex) prefix/ — empty = bucket root'
    : dstKind === 'objectstorage'
      ? shared.length > 0
        ? `ex) bucket/${connName}/`
        : 'ex) bucket/prefix/'
      : shared.length > 0
        ? `ex) /restore/${connName}`
        : 'ex) /home/ubuntu/restored';

  const cross = !!target && !!dstKind && dstKind !== category;
  const problem = crossTargetProblem(category, target, mapping.dstPath);

  return (
    <div className="space-y-5">
      <section className="space-y-3">
        <div className="grid md:grid-cols-2 gap-3">
          <Field label="source" hint="from the scan — read-only">
            <Select value={mapping.srcName} onChange={(e) => onChange({ srcName: e.target.value })}>
              {units.map((u) => (
                <option key={u.name} value={u.name}>
                  {u.name}
                </option>
              ))}
            </Select>
          </Field>

          <Field
            label="target"
            required={cross}
            hint={
              shared.length > 0
                ? `shared with ${shared.join(', ')} — give this one its own path`
                : osId
                  ? 'prefix in the selected bucket'
                  : cross
                    ? dstKind === 'filesystem'
                      ? 'cross-storage — an absolute path on the target host'
                      : 'cross-storage — bucket/prefix/ in the target'
                    : 'leave empty and the source name is used as-is'
            }
          >
            {osId ? (
              <div className="flex items-center gap-1 min-w-0">
                <span
                  className="shrink-0 text-sm font-mono text-text-muted"
                  title="The cm-beetle bucket chosen in Step 2. It is fixed: the bucket is decided by this osId."
                >
                  {osId}/
                </span>
                <TextInput
                  value={prefixAfterBucket(mapping.dstPath)}
                  onChange={(e) =>
                    onChange({ dstPath: e.target.value ? `${osId}/${e.target.value}` : '' })
                  }
                  placeholder={placeholder}
                />
              </div>
            ) : (
              <TextInput
                value={mapping.dstPath}
                onChange={(e) => onChange({ dstPath: e.target.value })}
                placeholder={placeholder}
              />
            )}
            {problem && (
              <span className="block mt-1 text-xs text-amber-600 dark:text-amber-400">⚠ {problem}</span>
            )}
          </Field>
        </div>

        {showStrategy && (
          <Field label="strategy">
            <div className="flex flex-wrap gap-6 pt-1">
              {strategies.map((s) => (
                <Radio
                  key={s}
                  name={`${category}-strategy-${connId}`}
                  value={s}
                  checked={mapping.strategy === s}
                  onChange={(v) => onChange({ strategy: v as Strategy })}
                  label={s}
                  hint={strategyHint(s)}
                />
              ))}
            </div>
          </Field>
        )}

        {category === 'objectstorage' && (
          <Note>
            A bucket path keeps its trailing slash exactly as the scan reported it. Editing it by
            hand is what makes a plan fail silently.
          </Note>
        )}
      </section>

      <section className="border-t border-border-main pt-4">
        <PathRuleEditor rules={mapping.pathRules} onChange={(pathRules) => onChange({ pathRules })} />
      </section>
    </div>
  );
}
