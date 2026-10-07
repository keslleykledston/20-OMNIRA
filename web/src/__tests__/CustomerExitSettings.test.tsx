import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import CustomerExitSettings from '../components/flows/CustomerExitSettings'

describe('CustomerExitSettings', () => {
  it('is off by default, shows the safeguards once on, and emits the commands the author typed', () => {
    const onChange = vi.fn()
    const { rerender } = render(<CustomerExitSettings onChange={onChange} />)
    expect(screen.queryByText(/sempre pede confirmação/)).toBeNull()
    fireEvent.click(screen.getByRole('checkbox'))
    expect(onChange).toHaveBeenLastCalledWith({ enabled: true })
    rerender(<CustomerExitSettings value={{ enabled: true }} onChange={onChange} />)
    expect(screen.getByText(/sempre pede confirmação/)).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText(/Comandos/), { target: { value: 'tchau, encerrar já' } })
    expect(onChange).toHaveBeenLastCalledWith({ enabled: true, commands: ['tchau', 'encerrar já'] })
  })

  it('cannot be edited in read-only mode', () => {
    render(<CustomerExitSettings value={{ enabled: true }} readOnly onChange={() => {}} />)
    expect(screen.getByRole('checkbox')).toBeDisabled()
  })
})
