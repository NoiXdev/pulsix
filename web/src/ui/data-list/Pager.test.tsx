import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { Pager, type PagerProps } from './Pager.tsx';

function renderPager(over: Partial<PagerProps> = {}) {
  const props: PagerProps = {
    page: 1,
    perPage: 25,
    total: 137,
    countLabel: (n) => `${n} hosts`,
    onPageChange: vi.fn(),
    onPerPageChange: vi.fn(),
    ...over,
  };
  render(<Pager {...props} />);
  return props;
}

describe('Pager', () => {
  it('states the range of the first page', () => {
    renderPager();
    expect(screen.getByText('1–25 of 137 hosts')).toBeInTheDocument();
  });

  it('states the range of the last, partial page', () => {
    renderPager({ page: 6 });
    expect(screen.getByText('126–137 of 137 hosts')).toBeInTheDocument();
  });

  it('states the range in German', () => {
    render(
      <I18nProvider locale="de">
        <Pager page={1} perPage={25} total={137} countLabel={(n) => `${n} Hosts`} onPageChange={vi.fn()} onPerPageChange={vi.fn()} />
      </I18nProvider>,
    );
    expect(screen.getByText('1–25 von 137 Hosts')).toBeInTheDocument();
  });

  it('changes the page size', async () => {
    const user = userEvent.setup();
    const props = renderPager();
    await user.click(screen.getByRole('combobox', { name: 'Rows per page' }));
    await user.click(screen.getByRole('option', { name: '50' }));
    expect(props.onPerPageChange).toHaveBeenCalledWith(50);
  });

  it('offers the page sizes 10, 25, 50 and 100', async () => {
    const user = userEvent.setup();
    renderPager();
    await user.click(screen.getByRole('combobox', { name: 'Rows per page' }));
    expect(screen.getAllByRole('option').map((o) => o.textContent)).toEqual(['10', '25', '50', '100']);
  });

  it('goes to a page by its number', async () => {
    const user = userEvent.setup();
    const props = renderPager();
    await user.click(screen.getByRole('button', { name: 'Page 2' }));
    expect(props.onPageChange).toHaveBeenCalledWith(2);
  });

  it('marks the current page and disables previous on the first page', () => {
    renderPager();
    expect(screen.getByRole('button', { name: 'Page 1' })).toHaveAttribute('aria-current', 'page');
    expect(screen.getByRole('button', { name: 'Previous page' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Next page' })).toBeEnabled();
  });

  it('disables next on the last page and steps back with previous', async () => {
    const user = userEvent.setup();
    const props = renderPager({ page: 6 });
    expect(screen.getByRole('button', { name: 'Next page' })).toBeDisabled();
    await user.click(screen.getByRole('button', { name: 'Previous page' }));
    expect(props.onPageChange).toHaveBeenCalledWith(5);
  });

  it('shows a gap instead of a long run of page numbers', () => {
    renderPager({ page: 1, perPage: 10, total: 137 });
    const nav = screen.getByRole('navigation', { name: 'Pages' });
    expect(nav).toHaveTextContent('…');
    expect(screen.queryByRole('button', { name: 'Page 7' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Page 14' })).toBeInTheDocument();
  });
});
