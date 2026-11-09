// Shrinks a chosen photo in the browser to a JPEG data URL within Supplier's
// 512 KiB limit, as Order does for delivery photos (order-service web/app.js).
const maxBytes = 512 * 1024;

export async function photoData(file: File): Promise<string> {
  if (!["image/jpeg", "image/png", "image/webp"].includes(file.type) || file.size > 20 * 1024 * 1024)
    throw new Error("Choose a JPEG, PNG or WebP image up to 20 MiB.");
  let bitmap: ImageBitmap;
  try { bitmap = await createImageBitmap(file); }
  catch { throw new Error("This image could not be read. Choose another photo."); }
  try {
    const scale = Math.min(1, 1600 / Math.max(bitmap.width, bitmap.height));
    const canvas = document.createElement("canvas");
    canvas.width = Math.max(1, Math.round(bitmap.width * scale));
    canvas.height = Math.max(1, Math.round(bitmap.height * scale));
    const context = canvas.getContext("2d");
    if (!context) throw new Error("This browser cannot prepare photos.");
    context.fillStyle = "white";
    context.fillRect(0, 0, canvas.width, canvas.height);
    context.drawImage(bitmap, 0, 0, canvas.width, canvas.height);
    for (const quality of [0.85, 0.7, 0.5, 0.3]) {
      const data = canvas.toDataURL("image/jpeg", quality);
      if (data.length - data.indexOf(",") - 1 <= 4 * Math.ceil(maxBytes / 3)) return data;
    }
    throw new Error("This photo is too large. Choose a smaller photo.");
  } finally {
    bitmap.close();
  }
}
