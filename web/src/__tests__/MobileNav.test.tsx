import { describe, expect, it } from 'vitest';
import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import MobileNav from '../components/MobileNav';
import { renderAt } from './testUtils';

// FRONTEND.2: mobile nav must reflect only real operational surfaces —
// Dashboard/Tickets (mock-backed) are gone entirely, in dev too. The rest of the sidebar's real
// surfaces (Grupos, Supervisor, Automação, Configurações…) live behind "Mais", not nowhere.

describe('MobileNav', () => {
  it('shows exactly the three direct shortcuts as links, plus "Mais"', () => {
    renderAt(<MobileNav />, '/inbox');
    const nav = screen.getByRole('navigation', { name: 'Navegação principal' });
    const links = nav.querySelectorAll('a');
    expect(links).toHaveLength(3);
    expect(screen.getByRole('link', { name: /Conversas/ })).toHaveAttribute('href', '/inbox');
    expect(screen.getByRole('link', { name: /Contatos/ })).toHaveAttribute('href', '/contacts');
    expect(screen.getByRole('link', { name: /Canais/ })).toHaveAttribute('href', '/channels');
    expect(screen.getByRole('button', { name: 'Mais' })).toBeInTheDocument();
  });

  it('does not render Dashboard or Tickets directly on the bar', () => {
    renderAt(<MobileNav />, '/inbox');
    expect(screen.queryByText('Dashboard')).not.toBeInTheDocument();
    expect(screen.queryByText('Tickets', { selector: 'nav a span' })).not.toBeInTheDocument();
  });

  it('"Mais" opens a drawer with the rest of the real surfaces (e.g. Grupos), not the pinned ones twice', async () => {
    renderAt(<MobileNav />, '/inbox');
    await userEvent.click(screen.getByRole('button', { name: 'Mais' }));
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByRole('link', { name: /Grupos/ })).toHaveAttribute('href', '/groups');
    expect(within(dialog).getByRole('link', { name: /^Tickets/ })).toHaveAttribute('href', '/tickets');
    expect(within(dialog).getByRole('link', { name: /Configurações/ })).toHaveAttribute('href', '/settings/general');
    // the three bottom-bar shortcuts are not duplicated inside the drawer
    expect(within(dialog).queryByRole('link', { name: 'Conversas' })).not.toBeInTheDocument();
    const closeButton = screen.getByRole('button', { name: 'Fechar' });
    await userEvent.click(closeButton);
    expect(dialog).not.toBeInTheDocument();
  });

  it('marks Contatos active on a contact detail child route', () => {
    renderAt(<MobileNav />, '/contacts/abc-123');
    expect(screen.getByRole('link', { name: /Contatos/ })).toHaveAttribute('aria-current', 'page');
    expect(screen.getByRole('link', { name: /Conversas/ })).not.toHaveAttribute('aria-current');
  });

  it('marks Canais active on the WAHA wizard child route', () => {
    renderAt(<MobileNav />, '/channels/whatsapp/new');
    expect(screen.getByRole('link', { name: /Canais/ })).toHaveAttribute('aria-current', 'page');
  });

  it('marks Conversas active on /inbox', () => {
    renderAt(<MobileNav />, '/inbox');
    expect(screen.getByRole('link', { name: /Conversas/ })).toHaveAttribute('aria-current', 'page');
  });
});
