export interface ImageSize {
  width: number
  height: number
}

/**
 * Fits an image into a measured box without cropping or changing its ratio.
 * The smaller of the horizontal and vertical scale factors is authoritative.
 */
export function fitImageSize(
  naturalWidth: number,
  naturalHeight: number,
  availableWidth: number,
  availableHeight: number,
): ImageSize | null {
  if (
    naturalWidth <= 0
    || naturalHeight <= 0
    || availableWidth <= 0
    || availableHeight <= 0
  ) return null

  const scale = Math.min(
    availableWidth / naturalWidth,
    availableHeight / naturalHeight,
  )
  return {
    width: Math.max(1, Math.floor(naturalWidth * scale)),
    height: Math.max(1, Math.floor(naturalHeight * scale)),
  }
}
