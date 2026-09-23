import { describe, it, expect } from 'vitest';
import { screen } from '@testing-library/react';
import { SettingsShell, SETTINGS_SECTIONS } from '../components/SettingsShell';
import { renderAt } from './testUtils';

describe('SettingsShell', () => {
  it('renders the section navigation with real routes', () => {
    renderAt(
      <SettingsShell sections={SETTINGS_SECTIONS} title="Equipe e acesso">
        <p>Conteúdo</p>
      </SettingsShell>,
      '/settings/team'
    );
    const nav = screen.getByRole('navigation', { name: 'Configurações' });
    expect(nav).toBeInTheDocument();
    for (const section of SETTINGS_SECTIONS) {
      const link = screen.getByRole('link', { name: section.label });
      expect(link).toHaveAttribute('href', section.href);
    }
  });

  it('marks the current route as aria-current=page', () => {
    renderAt(
      <SettingsShell sections={SETTINGS_SECTIONS} title="Agentes">
        <p>Conteúdo</p>
      </SettingsShell>,
      '/settings/agents'
    );
    expect(screen.getByRole('link', { name: 'Agentes' })).toHaveAttribute('aria-current', 'page');
    expect(screen.getByRole('link', { name: 'Equipe e acesso' })).not.toHaveAttribute('aria-current');
  });

  it('renders title, description and content region', () => {
    renderAt(
      <SettingsShell sections={SETTINGS_SECTIONS} title="Contas" description="Gerencie contas operacionais.">
        <p>Conteúdo real</p>
      </SettingsShell>,
      '/accounts'
    );
    expect(screen.getByRole('heading', { name: 'Contas' })).toBeInTheDocument();
    expect(screen.getByText('Gerencie contas operacionais.')).toBeInTheDocument();
    expect(screen.getByText('Conteúdo real')).toBeInTheDocument();
  });

  it('renders optional actions region', () => {
    renderAt(
      <SettingsShell sections={SETTINGS_SECTIONS} title="Contas" actions={<button>Nova conta</button>}>
        <p>Conteúdo</p>
      </SettingsShell>,
      '/accounts'
    );
    expect(screen.getByRole('button', { name: 'Nova conta' })).toBeInTheDocument();
  });
});
