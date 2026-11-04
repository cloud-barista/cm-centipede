'use client';

import React from 'react';
import { AppLayout } from '@/components/layout/AppLayout';
import { OverviewPage } from '@/components/overview/OverviewPage';
import { ConnectionsPage } from '@/components/connections/ConnectionsPage';
import { MigrationWizard } from '@/components/wizard/MigrationWizard';
import { RunsPage } from '@/components/runs/RunsPage';
import { useUiStore } from '@/store/uiStore';
import { CATEGORIES, type Category } from '@/lib/connectionCategory';

/**
 * Single-page tab routing through the ui store. The three
 * migration tabs are the same wizard bound to a different domain adapter.
 */
export default function Home() {
  const activeTab = useUiStore((s) => s.activeTab);
  const isWizard = (CATEGORIES as string[]).includes(activeTab);

  return (
    <AppLayout>
      {activeTab === 'overview' && <OverviewPage />}
      {activeTab === 'connections' && <ConnectionsPage />}
      {isWizard && <MigrationWizard key={activeTab} category={activeTab as Category} />}
      {activeTab === 'runs' && <RunsPage />}
    </AppLayout>
  );
}
