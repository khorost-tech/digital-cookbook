// Activity — обычный код без ограничений детерминизма.
export async function reserve(resource: string): Promise<string> {
  console.log(`>>> ACTIVITY reserve(${resource})`);
  return `res-${resource}-001`;
}

export async function allocate(reservationId: string): Promise<string> {
  console.log(`>>> ACTIVITY allocate(${reservationId})`);
  return `выделено ${reservationId}`;
}

export async function cancelReservation(reservationId: string): Promise<string> {
  console.log(`>>> ACTIVITY cancelReservation(${reservationId})`);
  return `снято ${reservationId}`;
}
