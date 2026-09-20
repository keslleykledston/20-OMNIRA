import { Icon } from '../../../components/primitives'
import type { QRImage } from '../../../lib/integrations'

interface Props {
  qr?: QRImage
  errorMessage?: string
}

export function QRCodeView({ qr, errorMessage }: Props) {
  return (
    <div className="flex items-center justify-center rounded-card border border-border-subtle bg-surface p-6">
      {qr ? (
        <img
          alt="QR Code para parear o WhatsApp"
          data-testid="qr-image"
          className="h-56 w-56 max-w-full"
          src={`data:${qr.mimetype};base64,${qr.data}`}
        />
      ) : (
        <div className="flex h-56 w-56 max-w-full flex-col items-center justify-center gap-3 rounded-card border border-dashed border-border-light px-4 text-center">
          {errorMessage ? (
            <>
              <span className="text-status-danger">
                <Icon name="close" size={24} />
              </span>
              <p className="text-body-sm text-text-secondary">{errorMessage}</p>
            </>
          ) : (
            <>
              <span className="h-7 w-7 animate-spin rounded-full border-2 border-accent-primary-soft border-t-accent-primary" />
              <p className="text-body-sm text-text-secondary">Gerando QR Code...</p>
            </>
          )}
        </div>
      )}
    </div>
  )
}

export function QRInstructions() {
  const steps = [
    'Abra o WhatsApp no seu celular.',
    'Toque em Dispositivos conectados.',
    'Toque em Conectar um dispositivo.',
    'Escaneie o código ao lado.',
  ]

  return (
    <div>
      <h3 className="text-section-sm font-semibold text-text-primary">Escaneie o QR Code</h3>
      <ol className="mt-4 space-y-3">
        {steps.map((text, i) => (
          <li key={text} className="flex gap-3">
            <span className="flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-full bg-accent-primary-soft text-metadata font-semibold text-accent-primary">
              {i + 1}
            </span>
            <span className="text-body-sm text-text-secondary">{text}</span>
          </li>
        ))}
      </ol>
    </div>
  )
}
