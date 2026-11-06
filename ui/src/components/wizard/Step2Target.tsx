'use client';

// Where each source connection goes. One row per connection in Step 1's scope:
// whether it is in the plan, and its destination — a reference cm-beetle
// already holds, or an inline target opened from the vault in Connections →
// Inline. A single connection is the same screen with one row and no
// checkbox.
//
// beetleDb needs an admin login cm-beetle never returns — the password, and
// since cm-beetle v0.6.2 the user name too — so it is collected here every time
// and parked in the secret store — never in the wizard draft and never in
// anything persisted.

import React from 'react';
import { Badge, Button, Checkbox, Field, Note, Select, TextArea, TextInput } from '@/components/ui/primitives';
import { neededNoun, optionText, presenceOf } from '@/components/connections/TargetShortlist';
import { useTargetPresence } from '@/queries/beetle';
import { useConnections, useRefinedScope } from '@/queries/honeybee';
import {
  INLINE_TARGET_STATUS_LABEL,
  inlineTargetFitsCategory,
  toConnectionRef,
} from '@/components/connections/inlineTarget';
import { ADAPTERS, entryOf } from '@/components/domains/adapters';
import { bootVault, targetPasswordKey, useSecretStore } from '@/store/secretStore';
import { routeOf, useWizardStore, type Route } from '@/store/wizardStore';
import { useTargetShortlist } from '@/store/targetShortlistStore';
import { locatorOf } from '@/lib/targetPresence';
import {
  TLS_MODES,
  tlsModeVerifies,
  type BeetleDBRef,
  type ConnectionRef,
  type TLSMode,
} from '@/types/common';
import { targetLabel } from '@/lib/targetLabel';
import { categoryOfRef, connectionAddress, type Category } from '@/lib/connectionCategory';
import type { ConnectionInfo } from '@/types/honeybee';

const isInline = (ref: ConnectionRef | null) =>
  ref?.source === 'ssh' || ref?.source === 'minio' || ref?.source === 'db';

/**
 * The destination field: cm-beetle references and inline targets as one select,
 * in two groups.
 *
 * Both used to be behind a Reference / Inline radio, which made one question
 * look like two. With a field per row the radio would have to be per row too;
 * one select answers it in one place.
 */
function TargetSelect({
  category,
  current,
  placeholder,
  onPick,
}: {
  category: Category;
  /** The route this field writes, or null for "apply to all". */
  current: Route | null;
  placeholder: string;
  onPick: (ref: ConnectionRef, label: string) => void;
}) {
  const adapter = ADAPTERS[category];
  const stored = useTargetShortlist((s) => s.items);
  const inlineTargets = useSecretStore((s) => s.targets);
  const rows = useSecretStore((s) => s.rows);

  // Narrowed to the kinds that can be a destination here, so the field never
  // offers a plan that cannot be built. An entry whose kind cannot be read is
  // kept rather than hidden (see TargetChoice).
  const items = React.useMemo(
    () =>
      stored.filter((i) => {
        const kind = locatorOf(i.ref)?.kind;
        return kind === undefined || adapter.targetTabs.includes(kind);
      }),
    [stored, adapter.targetTabs],
  );
  const check = useTargetPresence(items.map((i) => ({ key: i.label, ref: i.ref })));

  // Every stored inline target that fits, open or not: one that is still sealed is
  // listed, disabled, with the reason, so it does not look like it was lost.
  const usableInlineTargets = Object.values(rows).filter(
    (r) => r.preview && inlineTargetFitsCategory(r.preview, category),
  );
  const inlineTargetLabel = (r: { id: string; label?: string; source?: string }) =>
    targetLabel(r.source ?? '', r.label || r.id);

  // The select shows the current destination when it is one of its options.
  // One that is not — taken off the shortlist since, or an inline target now sealed —
  // still stands, and gets an option of its own so it is not shown as unset.
  let value = '';
  let orphan = false;
  if (current?.target) {
    if (items.some((i) => i.label === current.targetLabel)) value = `ref:${current.targetLabel}`;
    else {
      const d = usableInlineTargets.find((r) => inlineTargetLabel(r) === current.targetLabel);
      value = d ? `inline:${d.id}` : 'current';
      orphan = !d;
    }
  }

  return (
    <Select
      value={value}
      onChange={(e) => {
        const v = e.target.value;
        if (v.startsWith('ref:')) {
          const sel = items.find((i) => `ref:${i.label}` === v);
          // A gone entry still commits. One read that failed, or a resource
          // rebuilt under the same id, must not lock the reader out of their
          // own choice — the row says so instead.
          if (sel) onPick(sel.ref, sel.label);
        } else if (v.startsWith('inline:')) {
          const d = inlineTargets[v.slice('inline:'.length)];
          // Same label shape as a cm-beetle entry, so a row draws both alike.
          if (d) onPick(toConnectionRef(d), targetLabel(d.source, d.label || d.id));
        }
      }}
    >
      <option value="">{placeholder}</option>
      {orphan && current && <option value="current">{current.targetLabel}</option>}
      <optgroup label="Reference — cm-beetle">
        {items.length === 0 ? (
          <option disabled>No {neededNoun(adapter.targetTabs)}target set aside</option>
        ) : (
          items.map((item) => (
            <option key={item.label} value={`ref:${item.label}`}>
              {optionText(item.label, presenceOf(check, item.label))}
            </option>
          ))
        )}
      </optgroup>
      <optgroup label="Inline — vault">
        {usableInlineTargets.length === 0 ? (
          <option disabled>No inline target fits this category</option>
        ) : (
          usableInlineTargets.map((r) => (
            <option key={r.id} value={`inline:${r.id}`} disabled={r.status !== 'unlocked'}>
              {r.label || '(unnamed)'} — {r.source}
              {r.status !== 'unlocked' && ` (${INLINE_TARGET_STATUS_LABEL[r.status]})`}
            </option>
          ))
        )}
      </optgroup>
    </Select>
  );
}

export function Step2Target({ category, onDone }: { category: Category; onDone: () => void }) {
  const adapter = ADAPTERS[category];
  const state = useWizardStore((s) => s.byCategory[category]);
  const edit = useWizardStore((s) => s.edit);

  const rows = useSecretStore((s) => s.rows);
  const vaultUnlocked = useSecretStore((s) => s.unlocked);
  const secrets = useSecretStore((s) => s.wizardSecrets);
  const putWizardSecret = useSecretStore((s) => s.putWizardSecret);
  React.useEffect(bootVault, []);

  const sgId = state.source?.sgId ?? '';
  const connId = state.source?.connId ?? null;
  const groupMode = !connId;

  const conns = useConnections(sgId, !!sgId);
  // Step 1's scope, not the group as it is now: a connection added since was
  // never scanned, and Step 4 says so rather than this step routing it.
  const connIds = state.source?.connIds;
  const scope = React.useMemo(
    () => (conns.data?.connection_info ?? []).filter((c) => connIds?.includes(c.id)),
    [conns.data, connIds],
  );
  const refined = useRefinedScope(category, sgId || null, connId, !!sgId);

  const sealedInlineTargets = Object.values(rows).some(
    (r) => r.preview && inlineTargetFitsCategory(r.preview, category) && r.status !== 'unlocked',
  );

  /**
   * Each destination, checked against cm-beetle.
   *
   * The select marks its own options, but a destination does not have to be on
   * the shortlist — taking an entry off the list leaves the row pointing at it —
   * and this is where it matters most, one step before a plan is built against
   * it. It costs no request of its own: the same query keys back both.
   */
  const targetCheck = useTargetPresence(
    scope.flatMap((c) => {
      const t = routeOf(state, c.id).target;
      return t ? [{ key: c.id, ref: t }] : [];
    }),
  );

  /**
   * Writes rows' routes. A Step 2 edit: the plan and the run reference go.
   *
   * A destination of a different kind also takes its row's target path and
   * strategy: a path written for a host means nothing to a bucket, and the
   * strategy only exists between two hosts. The rest of the mapping — the unit
   * and the rules — is about the source, and stays.
   */
  function setRoutes(patches: Record<string, Partial<Route>>) {
    const routes = { ...state.routes };
    const entries = { ...state.entries };
    for (const [id, p] of Object.entries(patches)) {
      const before = routeOf(state, id);
      routes[id] = { ...before, ...p };
      if (
        'target' in p &&
        entries[id] &&
        categoryOfRef(p.target) !== categoryOfRef(before.target)
      ) {
        entries[id] = { ...entries[id], dstPath: '', strategy: 'direct' };
      }
    }
    edit(category, 2, { routes, entries });
  }

  /**
   * TLS settings go on the reference itself, not into the secret store beside
   * the password: a mode and a public root are not secrets, and buildPlanRequest
   * spreads the reference through, so whatever is set here reaches the plan.
   */
  function patchBeetleDb(id: string, p: Partial<BeetleDBRef>) {
    const t = routeOf(state, id).target;
    if (t?.source !== 'beetleDb' || !t.beetleDb) return;
    setRoutes({ [id]: { target: { ...t, beetleDb: { ...t.beetleDb, ...p } } } });
  }

  const secretOf = (id: string) => secrets[targetPasswordKey(category, id)];
  const needsPassword = (r: Route) => r.target?.source === 'beetleDb';
  const missingLogin = (id: string, r: Route) =>
    needsPassword(r) && (!secretOf(id)?.username.trim() || !secretOf(id)?.password.trim());

  const selected = scope.filter((c) => routeOf(state, c.id).selected);
  const withTarget = selected.filter((c) => routeOf(state, c.id).target);
  const ready =
    selected.length > 0 &&
    withTarget.length === selected.length &&
    !selected.some((c) => missingLogin(c.id, routeOf(state, c.id)));

  /** Fills only the selected rows still without a destination. */
  function applyToAll(ref: ConnectionRef, label: string) {
    const empty = selected.filter((c) => !routeOf(state, c.id).target);
    if (empty.length === 0) return;
    setRoutes(Object.fromEntries(empty.map((c) => [c.id, { target: ref, targetLabel: label }])));
  }

  function setAllSelected(on: boolean) {
    setRoutes(Object.fromEntries(scope.map((c) => [c.id, { selected: on }])));
  }

  function row(c: ConnectionInfo) {
    const route = routeOf(state, c.id);
    const entry = entryOf(adapter, refined.data, c.id);
    const gone = targetCheck.presence.get(c.id)?.state === 'gone';
    const cross = route.target && categoryOfRef(route.target) !== category;
    const secret = secretOf(c.id);
    const beetleDb = route.target?.source === 'beetleDb' ? route.target.beetleDb : undefined;

    return (
      <div key={c.id} className="space-y-2 border-b border-border-main/50 pb-3 last:border-0">
        <div className="grid md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] gap-x-4 gap-y-2 items-center">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs font-mono min-w-0">
            {groupMode ? (
              <Checkbox
                checked={route.selected}
                onChange={(v) => setRoutes({ [c.id]: { selected: v } })}
                label={c.name}
              />
            ) : (
              <span className="font-bold">{c.name}</span>
            )}
            <span className="text-text-muted">
              {connectionAddress(c, category)} · {adapter.srcNameOf(entry) || '(none)'}
            </span>
          </div>

          {route.selected ? (
            <div className="flex items-center gap-2 min-w-0">
              <span className="text-text-muted shrink-0">→</span>
              <div className="flex-1 min-w-0">
                <TargetSelect
                  category={category}
                  current={route}
                  placeholder="select target…"
                  onPick={(ref, label) => setRoutes({ [c.id]: { target: ref, targetLabel: label } })}
                />
              </div>
              {/* Beside the select, because it is a fact about the destination
                  picked there. Under the row it sat beneath the source's name,
                  on a line of its own, and read as something about the source. */}
              {cross && (
                <span
                  className="shrink-0"
                  title="The destination is the other storage type. The server derives dstType from it."
                >
                  <Badge tone="info">cross-storage</Badge>
                </span>
              )}
            </div>
          ) : (
            <span className="text-xs text-text-muted">→ (excluded)</span>
          )}
        </div>

        {/* Only what the select itself cannot say: the destination's name is
            already the select's value. */}
        {/* A missing login is not repeated here: the required fields just
            below say it, and the step will not advance without them. */}
        {route.selected && gone && (
          <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1 text-xs font-mono md:pl-6">
            {/* Said, not enforced. The destination is the reader's draft, and
                a single read of cm-beetle does not get to clear it. */}
            <span className="text-amber-600 dark:text-amber-400">
              ⚠ cm-beetle no longer lists this — planning against it will fail
            </span>
          </div>
        )}

        {route.selected && needsPassword(route) && (
          <div className="grid md:grid-cols-2 gap-3 md:pl-6">
            {/* Required: cm-beetle's RDBMS read has no adminUserName since
                v0.6.2, so there is no looked-up admin to fall back to. */}
            <Field label="username" required>
              <TextInput
                value={secret?.username ?? ''}
                onChange={(e) =>
                  putWizardSecret(targetPasswordKey(category, c.id), {
                    username: e.target.value,
                    password: secret?.password ?? '',
                  })
                }
              />
            </Field>
            {/* The password rides along in the plan and run requests, so it is
                held for the rest of this session only — in memory, apart from
                the inline targets, so it is neither listed as one nor written to
                their vault. */}
            <Field label="password" required>
              <TextInput
                type="password"
                value={secret?.password ?? ''}
                onChange={(e) =>
                  putWizardSecret(targetPasswordKey(category, c.id), {
                    username: secret?.username ?? '',
                    password: e.target.value,
                  })
                }
              />
            </Field>
            {/* Empty is "auto": centipede asks cm-beetle how the instance does
                TLS (what cb-spider measured) and connects to match, verifying
                against the CA cb-spider collected. A mode picked here is sent
                as is, so plaintext needs disable chosen explicitly, and only an
                explicit verify mode takes a tlsCAPem. */}
            <Field label="tlsMode" hint="empty = auto: decided from cm-beetle's report">
              <Select
                value={beetleDb?.tlsMode ?? ''}
                onChange={(e) => {
                  const tlsMode = (e.target.value || undefined) as TLSMode | undefined;
                  patchBeetleDb(
                    c.id,
                    tlsModeVerifies(tlsMode) ? { tlsMode } : { tlsMode, tlsCAPem: undefined },
                  );
                }}
              >
                <option value="">(auto)</option>
                {TLS_MODES.map((m) => (
                  <option key={m} value={m}>
                    {m}
                  </option>
                ))}
              </Select>
            </Field>
            {tlsModeVerifies(beetleDb?.tlsMode) && (
              <Field label="tlsCAPem" className="md:col-span-2">
                <TextArea
                  rows={3}
                  value={beetleDb?.tlsCAPem ?? ''}
                  onChange={(e) => patchBeetleDb(c.id, { tlsCAPem: e.target.value })}
                  placeholder="ex) -----BEGIN CERTIFICATE-----"
                />
              </Field>
            )}
          </div>
        )}
      </div>
    );
  }

  const anyPassword = selected.some((c) => needsPassword(routeOf(state, c.id)));
  const anyInline = selected.some((c) => isInline(routeOf(state, c.id).target));

  return (
    <div className="space-y-5">
      {category !== 'database' && (
        <Note>
          A {category === 'filesystem' ? 'file system' : 'object storage'} source may land on the
          other storage type — cross-storage. The server derives <code>dstType</code> from the
          destination chosen for it here.
        </Note>
      )}

      {/* Not for databases: a DBMS move is same-engine only, and a group can
          mix engines, so one destination for every row is rarely one any of
          them can take. Each row is picked on its own. */}
      {groupMode && scope.length > 1 && category !== 'database' && (
        <div className="flex flex-wrap items-center gap-3">
          <span className="text-xs text-text-muted shrink-0">apply to all selected</span>
          <div className="flex-1 min-w-48 max-w-md">
            {/* Fills only the rows still empty: a destination someone already
                chose for one host is not overwritten by a choice for all. */}
            <TargetSelect
              category={category}
              current={null}
              placeholder="select target…"
              onPick={applyToAll}
            />
          </div>
          <Button variant="ghost" onClick={() => setAllSelected(true)}>
            all
          </Button>
          <Button variant="ghost" onClick={() => setAllSelected(false)}>
            none
          </Button>
        </div>
      )}

      <div className="space-y-3">{scope.map(row)}</div>

      {groupMode && (
        <p className="text-xs text-text-muted">
          {selected.length} selected · {withTarget.length} target{withTarget.length === 1 ? '' : 's'}{' '}
          set. A connection left out here was still scanned in Step 1 — the whole group has to be.
        </p>
      )}

      {anyPassword && (
        <Note>
          cm-beetle never returns a managed instance’s admin user name or password, so there is
          nowhere to keep them. Enter them on their row — they are held for this browser session
          only.
        </Note>
      )}

      {/* Choosing copied the inline target's values into the wizard, so locking the
          vault afterwards takes nothing back from it. */}
      {anyInline && !vaultUnlocked && (
        <Note>
          The inline vault is locked. The destinations selected above stay valid — unlock only to
          pick a different inline target.
        </Note>
      )}
      {sealedInlineTargets && (
        <Note>
          Inline targets still sealed in the inline vault cannot be chosen. Unlock it in
          Connections → Inline.
        </Note>
      )}


      <div className="flex items-center gap-3 border-t border-border-main pt-4">
        <Button
          variant="primary"
          className="ml-auto"
          disabled={!ready}
          onClick={onDone}
        >
          next: mapping and filter →
        </Button>
      </div>
    </div>
  );
}
