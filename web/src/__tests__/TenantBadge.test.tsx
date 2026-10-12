import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { TenantBadge } from '../components/primitives/TenantBadge';
import { tenantCode } from '../lib/tenants';

// MT-AT-009: the company being served is recognised by a short code AND its full name, never by colour alone.
describe('TenantBadge', () => {
  it('shows a short code and the full name as text, and the colour is only decoration', () => {
    render(<TenantBadge name="ISP Roraima" />);
    expect(screen.getByText('ISP Roraima')).toBeInTheDocument(); // the full name is real text
    const code = tenantCode('ISP Roraima');
    expect(code.length).toBeGreaterThan(0);
    expect(code.length).toBeLessThanOrEqual(3);
    const codeEl = screen.getByText(code);
    expect(codeEl).toHaveAttribute('aria-hidden', 'true'); // the code is a visual aid; the name carries the meaning for assistive technology
    expect(codeEl.style.backgroundColor).toBe('var(--tenant-accent)'); // the accent layer is a token, not a palette of its own
  });

  it('two companies with different names never look identical by text', () => {
    const { container } = render(
      <>
        <TenantBadge name="ISP Roraima" />
        <TenantBadge name="NorteNet Telecom" />
      </>,
    );
    expect(container.textContent).toContain('ISP Roraima');
    expect(container.textContent).toContain('NorteNet Telecom');
  });
});
