/* Shared icon set — replaces the glyph-character buttons (‹ › ↑ ↻ ⚙ ≡ ▸ ·).
   Every icon is drawn on a 16px grid, stroke currentColor 1.5, round caps,
   so it inherits color from the surrounding text/button. */

import type { ReactNode, SVGProps } from "react";

export interface IconProps {
  size?: number;
  className?: string;
}

const STROKE = 1.5;

function base(size: number, className?: string): SVGProps<SVGSVGElement> {
  return {
    width: size,
    height: size,
    viewBox: "0 0 16 16",
    fill: "none",
    stroke: "currentColor",
    strokeWidth: STROKE,
    strokeLinecap: "round",
    strokeLinejoin: "round",
    className,
    "aria-hidden": true,
  };
}

function icon(children: ReactNode) {
  return function Icon({ size = 16, className }: IconProps) {
    return <svg {...base(size, className)}>{children}</svg>;
  };
}

export const Folder = icon(
  <path d="M1.75 4.25a1 1 0 0 1 1-1h3l1.5 1.8h6a1 1 0 0 1 1 1v5.7a1 1 0 0 1-1 1H2.75a1 1 0 0 1-1-1z" fill="currentColor" fillOpacity={0.22} />,
);

export const Disc = icon(
  <>
    <circle cx="8" cy="8" r="6.25" />
    <circle cx="8" cy="8" r="1.6" />
  </>,
);

export const Archive = icon(
  <>
    <rect x="1.75" y="3" width="12.5" height="3.25" rx="0.75" />
    <path d="M3 6.25v5.75a1 1 0 0 0 1 1h8a1 1 0 0 0 1-1V6.25" />
    <path d="M6.5 9h3" />
  </>,
);

export const File = icon(
  <>
    <path d="M3.5 2.75a1 1 0 0 1 1-1H9l3.5 3.5v8a1 1 0 0 1-1 1h-7a1 1 0 0 1-1-1z" />
    <path d="M9 1.75V5.5h3.75" />
  </>,
);

export const ChevronLeft = icon(<path d="M10 3.25 5.25 8 10 12.75" />);

export const ChevronRight = icon(<path d="M6 3.25 10.75 8 6 12.75" />);

export const ArrowUp = icon(
  <>
    <path d="M8 13.25V3" />
    <path d="M3.75 7.25 8 3l4.25 4.25" />
  </>,
);

export const Refresh = icon(
  <>
    <path d="M13.25 8a5.25 5.25 0 1 1-1.55-3.72" />
    <path d="M13.5 1.75v3h-3" />
  </>,
);

export const Search = icon(
  <>
    <circle cx="7" cy="7" r="4.5" />
    <path d="m10.4 10.4 3.6 3.6" />
  </>,
);

export const Sliders = icon(
  <>
    <path d="M2 4.5h5M11 4.5h3" />
    <circle cx="9" cy="4.5" r="1.6" />
    <path d="M2 11.5h2M8 11.5h6" />
    <circle cx="6" cy="11.5" r="1.6" />
  </>,
);

export const Pause = icon(<path d="M5.75 3.5v9M10.25 3.5v9" />);

export const Play = icon(<path d="M5 3.25 12.25 8 5 12.75z" />);

export const Close = icon(<path d="m4 4 8 8M12 4l-8 8" />);

export const Check = icon(<path d="m2.75 8.5 3.5 3.5 7-8" />);

export const Warning = icon(
  <>
    <path d="M8 2.25 14.75 13.5H1.25z" />
    <path d="M8 6.5v3.25" />
    <path d="M8 11.9h.01" />
  </>,
);

export const Bug = icon(
  <>
    <rect x="4.5" y="5.5" width="7" height="8" rx="3.5" />
    <path d="M6 5.5V4a2 2 0 0 1 4 0v1.5M2.5 9h2M11.5 9h2M3 12.5l1.8-1M13 12.5l-1.8-1M3 5.5l1.8 1M13 5.5l-1.8 1M8 5.5v8" />
  </>,
);

export const Heart = icon(
  <path d="M8 13.25S2.25 10 2.25 5.9c0-1.9 1.5-3.15 3.05-3.15 1.15 0 2.15.6 2.7 1.65.55-1.05 1.55-1.65 2.7-1.65 1.55 0 3.05 1.25 3.05 3.15 0 4.1-5.75 7.35-5.75 7.35z" />,
);

export const Monitor = icon(
  <>
    <rect x="1.75" y="2.75" width="12.5" height="8.5" rx="1" />
    <path d="M6 14h4M8 11.25V14" />
  </>,
);

export const Tree = icon(
  <>
    <path d="M4.5 2.5v8.25a1 1 0 0 0 1 1H8" />
    <path d="M4.5 6.75H8" />
    <rect x="9.75" y="5" width="4.5" height="3.5" rx="0.75" />
    <rect x="9.75" y="10" width="4.5" height="3.5" rx="0.75" />
    <rect x="2.25" y="1.5" width="4.5" height="3.5" rx="0.75" />
  </>,
);

export interface SlipstreamProps extends IconProps {
  /** Color of the trailing chevron (the wake). Defaults to the dim accent. */
  trail?: string;
  /** Color of the leading chevron. Defaults to the accent. */
  accent?: string;
}

/** The two-chevron brand glyph: a leading chevron and its slipstream. */
export function Slipstream({
  size = 16,
  className,
  trail = "var(--accent-dim)",
  accent = "var(--accent)",
}: SlipstreamProps) {
  return (
    <svg {...base(size, className)} strokeWidth={1.7}>
      <path d="M3 3.5 7.5 8 3 12.5" stroke={trail} />
      <path d="M8.5 3.5 13 8l-4.5 4.5" stroke={accent} />
    </svg>
  );
}

export const Shrink = icon(
  <>
    <path d="M6.5 1.5v5h-5" />
    <path d="M9.5 14.5v-5h5" />
  </>,
);

export const Expand = icon(
  <>
    <path d="M9.5 1.5h5v5" />
    <path d="M6.5 14.5h-5v-5" />
  </>,
);
