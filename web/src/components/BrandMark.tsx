// Marca do OMNIRA (círculo com balão de conversa e quatro nós). O arquivo vetorial é servido de /omnira.svg
// (public/), o mesmo usado como favicon; este componente só o posiciona. `label` vazio = decorativa (o nome
// já aparece em texto ao lado); com texto, vira a descrição acessível da imagem.
export function BrandMark({ size = 32, label = '', className = '' }: { size?: number; label?: string; className?: string }) {
  return (
    <img
      src="/omnira.svg"
      width={size}
      height={size}
      alt={label}
      aria-hidden={label === '' ? true : undefined}
      draggable={false}
      className={`flex-shrink-0 select-none ${className}`.trim()}
    />
  )
}
