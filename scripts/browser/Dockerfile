FROM mcr.microsoft.com/playwright:v1.62.1-noble
WORKDIR /checks
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts
COPY check.cjs ./
CMD ["node", "check.cjs"]
