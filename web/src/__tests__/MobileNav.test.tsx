import { describe, expect, it } from 'vitest';
import { screen } from '@testing-library/react';
import MobileNav from '../components/MobileNav';
import { renderAt } from './testUtils';

// FRONTEND.2: mobile nav must reflect only real operational surfaces —
// Dashboard/Tickets (mock-backed) are gone entirely, in dev too.

describe('MobileNav', () => {
  it('shows exactly the three real operational surfaces, no more, no less', () => {
    renderAt(<MobileNav />, '/inbox');
    const nav = screen.getByRole('navigation', { name: 'Navegação principal' });
    const links = nav.querySelectorAll('a');
    expect(links).toHaveLength(3);
    expect(screen.getByRole('link', { name: /Conversas/ })).toHaveAttribute('href', '/inbox');
    expect(screen.getByRole('link', { name: /Contatos/ })).toHaveAttribute('href', '/contacts');
    expect(screen.getByRole('link', { name: /Canais/ })).toHaveAttribute('href', '/channels');
  });

  it('does not render Dashboard, Tickets, or a "Mais" placeholder', () => {
    renderAt(<MobileNav />, '/inbox');
    expect(screen.queryByText('Dashboard')).not.toBeInTheDocument();
    expect(screen.queryByText('Tickets')).not.toBeInTheDocument();
    expect(screen.queryByText('Mais')).not.toBeInTheDocument();
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
