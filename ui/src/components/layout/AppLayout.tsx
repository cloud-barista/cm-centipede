'use client';

// Three nav groups (Dashboard / Data Migration / Operations).

import React from 'react';
import {
  Database,
  GitFork,
  HardDrive,
  ListChecks,
  Maximize2,
  Moon,
  Network,
  Server,
  Sun,
} from 'lucide-react';
import { useUiStore, type TabId } from '@/store/uiStore';
import { cx } from '@/lib/format';

interface NavItem {
  id: TabId;
  label: string;
  icon: React.ComponentType<{ className?: string }>;
  desc: string;
  accent: string;
}

const dashboardNavItems: NavItem[] = [
  {
    id: 'overview',
    label: 'Overview',
    icon: Server,
    desc: 'Migration runs, categories and recent activity',
    accent: 'indigo',
  },
  {
    id: 'connections',
    label: 'Connections',
    icon: Network,
    desc: 'Sources, target references and inline targets',
    accent: 'indigo',
  },
];

const migrationNavItems: NavItem[] = [
  {
    id: 'filesystem',
    label: 'File System',
    icon: HardDrive,
    desc: 'Folder transfer between SSH hosts',
    accent: 'sky',
  },
  {
    id: 'objectstorage',
    label: 'Object Storage',
    icon: Database,
    desc: 'Bucket transfer between S3-compatible stores',
    accent: 'amber',
  },
  {
    id: 'database',
    label: 'Database',
    icon: Database,
    desc: 'Schema and data transfer between DBMS servers',
    accent: 'violet',
  },
];

const operationsNavItems: NavItem[] = [
  {
    id: 'runs',
    label: 'Runs',
    icon: ListChecks,
    desc: 'History, logs, retry and validation',
    accent: 'indigo',
  },
];

const ACCENT_ACTIVE: Record<string, string> = {
  indigo: 'border-indigo-500 bg-bg-panel text-indigo-600 dark:text-indigo-400',
  sky: 'border-sky-500 bg-bg-panel text-sky-500 dark:text-sky-400',
  amber: 'border-amber-500 bg-bg-panel text-amber-600 dark:text-amber-400',
  violet: 'border-violet-500 bg-bg-panel text-violet-500 dark:text-violet-400',
};

/** Which edge a nav button's tooltip is pinned to. */
type TooltipAlign = 'left' | 'right';

function NavButton({
  item,
  active,
  onClick,
  tooltipAlign = 'left',
}: {
  item: NavItem;
  active: boolean;
  onClick: () => void;
  tooltipAlign?: TooltipAlign;
}) {
  const Icon = item.icon;
  const tooltipId = `nav-desc-${item.id}`;
  return (
    <button
      type="button"
      onClick={onClick}
      aria-describedby={tooltipId}
      className={cx(
        'relative flex items-start p-3 rounded-xl text-left border transition-all duration-300 group cursor-pointer',
        active
          ? ACCENT_ACTIVE[item.accent]
          : 'border-border-main bg-bg-main/20 text-text-main hover:border-indigo-500/30 hover:bg-bg-panel',
      )}
    >
      <div
        className={cx(
          'p-2 rounded-lg mr-3 border shrink-0',
          active ? 'bg-bg-input border-border-main' : 'bg-bg-input border-transparent text-text-muted',
        )}
      >
        <Icon className="w-4 h-4" />
      </div>
      <div className="min-w-0">
        <h3 className="font-extrabold text-sm">{item.label}</h3>
        {/* Kept to one line so the nav keeps its height; the tooltip below is
            what makes the rest of the sentence reachable. */}
        <p className="text-xs text-text-muted mt-0.5 leading-snug truncate">{item.desc}</p>
      </div>

      {/* Pinned to whichever edge keeps it inside the viewport: body carries
          overflow-x: hidden, so a tooltip running off the right would be
          clipped away silently rather than scrolled to.
          focus-visible, not focus — otherwise it would stay open after a click. */}
      <span
        id={tooltipId}
        role="tooltip"
        className={cx(
          'pointer-events-none absolute top-full z-50 mt-1.5 hidden w-max max-w-[17rem]',
          'rounded-lg border border-border-input bg-bg-panel px-2.5 py-1.5 shadow-lg',
          'text-xs leading-snug text-text-main',
          'group-hover:block group-focus-visible:block',
          tooltipAlign === 'right' ? 'right-0' : 'left-0',
        )}
      >
        {item.desc}
      </span>
    </button>
  );
}

function NavGroup({
  title,
  items,
  activeTab,
  setActiveTab,
  cols,
  grow,
  tooltipAlign,
}: {
  title: string;
  items: NavItem[];
  activeTab: TabId;
  setActiveTab: (t: TabId) => void;
  cols: string;
  grow: string;
  tooltipAlign?: TooltipAlign;
}) {
  return (
    <div className={cx('border border-border-main rounded-2xl p-3 bg-bg-main/30', grow)}>
      <div className="flex items-center mb-2 px-1">
        <span className="px-2.5 py-1 text-2xs font-extrabold text-indigo-600 dark:text-indigo-400 bg-indigo-500/10 border border-indigo-500/20 rounded-md font-mono">
          {title}
        </span>
      </div>
      <nav className={cx('grid gap-3', cols)}>
        {items.map((item) => (
          <NavButton
            key={item.id}
            item={item}
            active={activeTab === item.id}
            onClick={() => setActiveTab(item.id)}
            tooltipAlign={tooltipAlign}
          />
        ))}
      </nav>
    </div>
  );
}

export function AppLayout({ children }: { children: React.ReactNode }) {
  const { activeTab, setActiveTab, themeMode, toggleTheme, layoutWidth, setLayoutWidth } =
    useUiStore();

  React.useEffect(() => {
    const root = window.document.documentElement;
    root.classList.toggle('light', themeMode === 'light');
    root.classList.toggle('dark', themeMode !== 'light');
  }, [themeMode]);

  return (
    <div className="min-h-screen bg-bg-main text-text-main flex flex-col">
      <header className="sticky top-0 z-40 w-full border-b border-border-main bg-bg-main/85 backdrop-blur-md">
        <div className="w-full max-w-[1920px] mx-auto px-6 h-16 flex items-center justify-between">
          <div className="flex items-center space-x-3">
            <div className="p-2 bg-gradient-to-tr from-indigo-500 to-violet-500 rounded-lg">
              <GitFork className="w-5 h-5 text-white" />
            </div>
            <div>
              <span className="font-extrabold text-xl tracking-tight bg-gradient-to-r from-indigo-400 to-violet-400 bg-clip-text text-transparent">
                Centipede UX Lab
              </span>
              {/* Name and version as one literal. Nothing links it to
                  ui/package.json, so a release bump has to come through here. */}
              <span className="ml-2.5 text-2xs font-semibold px-2 py-0.5 bg-bg-input border border-border-main rounded-full text-text-muted font-mono">
                CM-Centipede v0.1.0
              </span>
            </div>
          </div>

          <div className="flex items-center space-x-3">
            <div className="hidden lg:flex items-center space-x-2 bg-bg-panel border border-border-main px-2.5 py-1 rounded-xl">
              <Maximize2 className="w-3.5 h-3.5 text-indigo-600 dark:text-indigo-400" />
              <span className="text-xs font-bold text-text-muted font-mono whitespace-nowrap">
                {layoutWidth}px
              </span>
              <input
                type="range"
                min="1200"
                max="2560"
                step="40"
                value={layoutWidth}
                onChange={(e) => setLayoutWidth(Number(e.target.value))}
                className="w-24 accent-indigo-500 cursor-pointer"
                title="Adjust layout width"
              />
            </div>

            <button
              type="button"
              onClick={toggleTheme}
              aria-label="Toggle theme"
              className="p-2 bg-bg-panel border border-border-main hover:border-indigo-500/40 rounded-xl transition text-indigo-600 dark:text-indigo-400 cursor-pointer"
            >
              {themeMode === 'dark' ? <Sun className="w-4 h-4" /> : <Moon className="w-4 h-4" />}
            </button>
          </div>
        </div>
      </header>

      <div className="bg-bg-panel border-b border-border-main">
        <div
          className="mx-auto px-6 py-3 flex flex-wrap items-stretch gap-4"
          style={{ width: `${layoutWidth}px`, maxWidth: '100%' }}
        >
          <NavGroup
            title="Dashboard"
            items={dashboardNavItems}
            activeTab={activeTab}
            setActiveTab={setActiveTab}
            cols="grid-cols-1 md:grid-cols-2"
            grow="flex-[2] min-w-[420px]"
          />
          <NavGroup
            title="Data Migration"
            items={migrationNavItems}
            activeTab={activeTab}
            setActiveTab={setActiveTab}
            cols="grid-cols-1 md:grid-cols-3"
            grow="flex-[3] min-w-[560px]"
          />
          <NavGroup
            title="Operations"
            items={operationsNavItems}
            activeTab={activeTab}
            setActiveTab={setActiveTab}
            cols="grid-cols-1"
            grow="flex-[1] min-w-[220px]"
            // Last group on the row, and its buttons are the narrowest — a
            // left-pinned tooltip would run past the right edge and be clipped.
            tooltipAlign="right"
          />
        </div>
      </div>

      <main
        className="flex-1 mx-auto px-6 py-8"
        style={{ width: `${layoutWidth}px`, maxWidth: '100%' }}
      >
        <div className="animate-fade-in">{children}</div>
      </main>

      <footer className="border-t border-border-main bg-bg-panel py-5 text-center text-xs text-text-muted">
        <p>© 2026 Cloud-Barista Authors. Licensed under the Apache License, Version 2.0.</p>
      </footer>
    </div>
  );
}
