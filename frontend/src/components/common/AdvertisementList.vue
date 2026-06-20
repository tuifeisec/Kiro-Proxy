<script setup>
import { onMounted, ref } from 'vue'
import { getAdvertisements, openExternalURL } from '../../api/license.js'

const advertisements = ref([])

async function loadAdvertisements() {
  try {
    const ads = await getAdvertisements()
    advertisements.value = Array.isArray(ads) ? ads : []
  } catch {
    advertisements.value = []
  }
}

async function openAdvertisement(ad) {
  const url = String(ad?.link_url || '').trim()
  if (!url) return
  try {
    await openExternalURL(url)
  } catch {
    window.open(url, '_blank', 'noopener,noreferrer')
  }
}

function advertisementText(ad) {
  return [ad?.title, ad?.description].filter(Boolean).join('：') || '广告'
}

function advertisementKey(ad, index) {
  return ad?.id || `${ad?.title || 'ad'}-${index}`
}

function advertisementImageStyle(ad) {
  return ad?.image_url ? { backgroundImage: `url(${ad.image_url})` } : {}
}

onMounted(loadAdvertisements)
</script>

<template>
  <section v-if="advertisements.length" class="grid grid-cols-1 gap-2 sm:grid-cols-2 xl:grid-cols-3">
    <button
      v-for="(ad, index) in advertisements"
      :key="advertisementKey(ad, index)"
      type="button"
      class="group h-[96px] overflow-hidden rounded-xl border border-[#e2e8f0] bg-white text-left shadow-[0_1px_3px_rgba(15,23,42,0.04)] transition hover:border-[#bfdbfe] hover:shadow-[0_8px_20px_rgba(37,99,235,0.08)]"
      :class="ad.link_url ? 'cursor-pointer' : 'cursor-default'"
      @click="openAdvertisement(ad)"
    >
      <div
        class="flex h-full items-center justify-center bg-[#f8fafc] bg-cover bg-center px-4 text-center"
        :style="advertisementImageStyle(ad)"
      >
        <div
          v-if="!ad.image_url"
          class="line-clamp-2 text-[14px] font-semibold leading-5 text-[#334155]"
        >
          {{ advertisementText(ad) }}
        </div>
      </div>
    </button>
  </section>
</template>
